package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type mediaInput struct {
	Kind, Provider, Model, Text, Prompt, Voice, Image, Sample, Filename string
	Consent                                                             bool
}

func field(m obj, k ...string) any {
	var v any = m
	for _, s := range k {
		o, _ := v.(map[string]any)
		v = o[s]
	}
	return v
}
func str(m obj, k ...string) string {
	v := field(m, k...)
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	}
	return ""
}
func responseObject(res *http.Response) (obj, error) {
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return nil, upstreamErr(res.StatusCode)
	}
	var d obj
	dec := json.NewDecoder(io.LimitReader(res.Body, 96<<20))
	dec.UseNumber()
	if dec.Decode(&d) != nil {
		return nil, fault{502, "厂商返回了无法解析的响应"}
	}
	return d, checkStatus(d)
}
func checkStatus(d obj) error {
	if c := str(d, "base_resp", "status_code"); c != "" && c != "0" {
		reason := map[string]string{"1002": "请求过于频繁", "1004": "鉴权失败", "1008": "账号余额不足", "2013": "模型或输入参数不适用", "2038": "账号权限不足"}[c]
		return fault{502, "厂商业务错误 " + c + " " + reason}
	}
	return nil
}
func (a *app) mediaAPI(ctx context.Context, p, method, path string, data obj) (obj, error) {
	var body io.Reader
	if data != nil {
		b, e := json.Marshal(data)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
	}
	res, e := a.upstream(ctx, p, method, path, "application/json", body)
	if e != nil {
		return nil, fault{502, "厂商连接中断或超时，请核对任务状态"}
	}
	return responseObject(res)
}
func (a *app) jobs(w http.ResponseWriter, r *http.Request) error {
	v, e := rows(a.db, "SELECT id,created,kind,provider,model,state,remote,voice,note,usage,asset FROM jobs ORDER BY created DESC")
	if e != nil {
		return e
	}
	return jsonOut(w, v)
}
func (p *mediaInput) validate() error {
	if strings.TrimSpace(p.Text) == "" && p.Kind != "video" {
		return bad("请输入文案或歌词")
	}
	if p.Kind == "video" {
		if strings.TrimSpace(p.Prompt) == "" {
			return bad("请输入视频描述")
		}
		if p.Provider == "ark" {
			if p.Model == "" {
				p.Model = "doubao-seedance-2-0-260128"
			}
		} else if p.Provider == "minimax" {
			if p.Model == "" {
				p.Model = "MiniMax-H3-Lite"
			}
			if !slices.Contains([]string{"MiniMax-H3", "MiniMax-H3-Lite", "MiniMax-H3-Highspeed"}, p.Model) {
				return bad("视频模型无效")
			}
		} else {
			return bad("视频渠道无效")
		}
		if p.Image != "" && !strings.HasPrefix(p.Image, "data:image/jpeg;base64,") && !strings.HasPrefix(p.Image, "data:image/png;base64,") {
			return bad("首帧请选择 PNG 或 JPEG 图片")
		}
		return nil
	}
	p.Provider = "minimax"
	switch p.Kind {
	case "speech", "stream", "long":
		if p.Voice == "" {
			p.Voice = "male-qn-qingse"
		}
		p.Model = "speech-2.8-turbo"
	case "music":
		p.Model = "music-3.0"
		if strings.TrimSpace(p.Prompt) == "" {
			return bad("请输入音乐风格")
		}
	case "design":
		p.Model = "voice_design"
		if strings.TrimSpace(p.Prompt) == "" {
			return bad("请输入音色描述")
		}
	case "clone":
		p.Model = "speech-2.8-turbo"
		if !p.Consent || p.Sample == "" {
			return bad("需要获授权的样音和使用确认")
		}
		if !slices.Contains([]string{".mp3", ".wav", ".m4a"}, strings.ToLower(filepath.Ext(p.Filename))) {
			return bad("请选择 MP3、WAV 或 M4A 样音")
		}
	default:
		return bad("任务类型无效")
	}
	return nil
}
func (a *app) newJob(p mediaInput) (string, error) {
	id := "job_" + token(10)
	_, e := a.db.Exec("INSERT INTO jobs(id,kind,provider,model,state,created,custom_voice) VALUES(?,?,?,?,'submitting',?,EXISTS(SELECT 1 FROM jobs WHERE voice=? AND voice!=''))", id, p.Kind, p.Provider, p.Model, time.Now().UnixMilli(), p.Voice)
	return id, e
}
func (a *app) jobError(id string, e error) {
	note := "任务处理失败，请按厂商记录核实"
	var f fault
	if errors.As(e, &f) {
		note = f.msg
	}
	state := "unknown"
	if strings.Contains(note, "HTTP 4") || strings.Contains(note, "厂商业务错误") {
		state = "failed"
	}
	a.db.Exec("UPDATE jobs SET state=?,note=? WHERE id=?", state, note, id)
}
func (a *app) createJob(w http.ResponseWriter, r *http.Request) error {
	var p mediaInput
	if e := readBody(w, r, &p, 32<<20); e != nil {
		return e
	}
	if e := p.validate(); e != nil {
		return e
	}
	if p.Kind == "stream" {
		return bad("请使用流式朗读入口")
	}
	if p.Kind == "music" && os.Getenv("MUSIC3_BASE_URL") != "" {
		p.Provider = "music3"
		p.Model = "MiniMaxAI/MiniMax-Music3"
	}
	if p.Provider != "music3" && a.providers[p.Provider].key == "" {
		return bad("渠道尚未配置凭证")
	}
	id, e := a.newJob(p)
	if e != nil {
		return e
	}
	w.Header().Set("X-Request-ID", id)
	e = a.submitMedia(r.Context(), id, p)
	if e != nil {
		a.jobError(id, e)
		return e
	}
	return jsonOut(w, obj{"id": id})
}
func speechPayload(p mediaInput) obj {
	return obj{"model": p.Model, "text": p.Text, "voice_setting": obj{"voice_id": p.Voice}, "audio_setting": obj{"format": "mp3", "sample_rate": 32000, "bitrate": 128000, "channel": 1}}
}
func (a *app) submitMedia(ctx context.Context, id string, p mediaInput) error {
	if p.Provider == "music3" {
		body, _ := json.Marshal(obj{"model": p.Model, "input": p.Text, "instructions": p.Prompt, "response_format": "wav", "stream": false, "max_new_tokens": 750})
		req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(os.Getenv("MUSIC3_BASE_URL"), "/")+"/v1/audio/speech", bytes.NewReader(body))
		if e != nil {
			return bad("自托管音乐服务地址无效")
		}
		req.Header.Set("Content-Type", "application/json")
		res, e := a.client.Do(req)
		if e != nil {
			return fault{502, "自托管音乐服务连接失败"}
		}
		defer res.Body.Close()
		if res.StatusCode/100 != 2 {
			return upstreamErr(res.StatusCode)
		}
		return a.storeAsset(id, ".wav", res.Body, "")
	}
	path := ""
	var data obj
	voice := ""
	switch p.Kind {
	case "video":
		content := []obj{{"type": "text", "text": p.Prompt}}
		if p.Image != "" {
			content = append(content, obj{"type": "image_url", "image_url": obj{"url": p.Image}, "role": "first_frame"})
		}
		path = "/v2/video_generation"
		data = obj{"model": p.Model, "content": content, "duration": 4, "resolution": "480P"}
		if p.Image == "" {
			data["ratio"] = "16:9"
		}
		if p.Provider == "ark" {
			path = "/contents/generations/tasks"
			data["resolution"] = "480p"
			data["generate_audio"] = false
			data["duration"] = 4
		} else if p.Model == "MiniMax-H3" {
			data["resolution"] = "768P"
		}
	case "long":
		path = "/v1/t2a_async_v2"
		data = speechPayload(p)
	case "speech":
		path = "/v1/t2a_v2"
		data = speechPayload(p)
	case "music":
		path = "/v1/music_generation"
		data = obj{"model": p.Model, "prompt": p.Prompt, "lyrics": p.Text, "output_format": "hex"}
	case "design":
		path = "/v1/voice_design"
		data = obj{"prompt": p.Prompt, "preview_text": p.Text}
	case "clone":
		file, e := base64.StdEncoding.DecodeString(p.Sample)
		if e != nil || len(file) == 0 || len(file) > 20<<20 {
			return bad("样音格式或大小无效")
		}
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		mw.WriteField("purpose", "voice_clone")
		part, e := mw.CreateFormFile("file", "sample"+strings.ToLower(filepath.Ext(p.Filename)))
		if e != nil {
			return e
		}
		part.Write(file)
		mw.Close()
		res, e := a.upstream(ctx, "minimax", "POST", "/v1/files/upload", mw.FormDataContentType(), &b)
		if e != nil {
			return fault{502, "样音上传失败"}
		}
		d, e := responseObject(res)
		if e != nil {
			return e
		}
		fid := field(d, "file", "file_id")
		if fid == nil {
			return fault{502, "厂商未返回文件 ID"}
		}
		voice = "gate" + token(10)
		path = "/v1/voice_clone"
		data = obj{"file_id": fid, "voice_id": voice, "text": p.Text, "model": p.Model}
	}
	d, e := a.mediaAPI(ctx, p.Provider, "POST", path, data)
	if e != nil {
		var f fault
		if p.Kind == "music" && errors.As(e, &f) && strings.Contains(f.msg, "410") {
			return fault{502, "厂商已停止向此账号提供音乐接口（HTTP 410）。可配置自托管 MiniMax Music 3 服务。"}
		}
		return e
	}
	if p.Kind == "video" || p.Kind == "long" {
		remote := str(d, "task_id")
		if remote == "" {
			remote = str(d, "id")
		}
		if remote == "" {
			return fault{502, "厂商未返回任务 ID"}
		}
		var usage string
		if chars := field(d, "usage_characters"); chars != nil {
			b, _ := json.Marshal(obj{"usage_characters": chars})
			usage = string(b)
		}
		_, e = a.db.Exec("UPDATE jobs SET state='processing',remote=?,usage=? WHERE id=?", remote, usage, id)
		return e
	}
	audio := str(d, "data", "audio")
	if p.Kind == "design" {
		voice = str(d, "voice_id")
		audio = str(d, "trial_audio")
	}
	if p.Kind == "clone" {
		audio = str(d, "demo_audio")
	}
	if (p.Kind == "clone" || p.Kind == "design") && voice == "" {
		return fault{502, "厂商未返回音色 ID"}
	}
	if voice != "" {
		if _, e = a.db.Exec("UPDATE jobs SET voice=? WHERE id=?", voice, id); e != nil {
			return e
		}
	}
	var reader io.Reader
	if strings.HasPrefix(audio, "https://") {
		res, e := a.download(ctx, audio)
		if e != nil {
			return e
		}
		defer res.Body.Close()
		reader = res.Body
	} else {
		reader = hex.NewDecoder(strings.NewReader(audio))
	}
	return a.storeAsset(id, ".mp3", reader, usageInfo(d))
}
func usageInfo(d obj) string {
	for _, key := range []string{"usage", "extra_info"} {
		if o, ok := d[key].(map[string]any); ok {
			summary := obj{}
			for k, v := range o {
				if _, ok := v.(json.Number); ok {
					summary[k] = v
				}
			}
			b, _ := json.Marshal(summary)
			return string(b)
		}
	}
	return ""
}
func (a *app) download(ctx context.Context, raw string) (*http.Response, error) {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, fault{502, "厂商未返回有效的下载地址"}
	}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return nil, e
	}
	res, e := a.client.Do(req)
	if e != nil {
		return nil, fault{502, "访问厂商成品失败"}
	}
	if res.StatusCode/100 != 2 {
		res.Body.Close()
		return nil, upstreamErr(res.StatusCode)
	}
	return res, nil
}
func (a *app) storeAsset(id, ext string, r io.Reader, usage string) error {
	if e := os.MkdirAll(a.dir, 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(a.dir, ".partial-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	n, e := io.Copy(f, io.LimitReader(r, (256<<20)+1))
	closeErr := f.Close()
	if e != nil || closeErr != nil || n == 0 || n > 256<<20 {
		return fault{502, "成品为空、过大或下载中断"}
	}
	name := id + ext
	if e = os.Rename(f.Name(), filepath.Join(a.dir, name)); e != nil {
		return e
	}
	_, e = a.db.Exec("UPDATE jobs SET state='done',asset=?,usage=coalesce(nullif(?,''),usage),note='' WHERE id=?", name, usage, id)
	return e
}
func (a *app) asset(w http.ResponseWriter, r *http.Request) error {
	var name string
	if e := a.db.QueryRow("SELECT asset FROM jobs WHERE id=? AND state='done'", r.PathValue("id")).Scan(&name); e != nil || name == "" {
		return fault{404, "成品暂不可用"}
	}
	if r.URL.Query().Has("download") {
		w.Header().Set("Content-Disposition", "attachment; filename="+name)
	}
	http.ServeFile(w, r, filepath.Join(a.dir, name))
	return nil
}
func (a *app) worker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			list, e := rows(a.db, "SELECT id,remote,kind,provider FROM jobs WHERE state='processing' ORDER BY created")
			if e != nil {
				continue
			}
			for _, j := range list {
				if e = a.poll(ctx, j); e != nil {
					a.db.Exec("UPDATE jobs SET note=? WHERE id=? AND state='processing'", "查询或下载暂时失败，将自动重试", j["id"])
				}
			}
		}
	}
}
func (a *app) poll(ctx context.Context, j obj) error {
	id, p, remote := str(j, "id"), str(j, "provider"), url.PathEscape(str(j, "remote"))
	path := "/v2/query/video_generation/" + remote
	if p == "ark" {
		path = "/contents/generations/tasks/" + remote
	}
	if str(j, "kind") == "long" {
		path = "/v1/query/t2a_async_query_v2?task_id=" + remote
	}
	d, e := a.mediaAPI(ctx, p, "GET", path, nil)
	if e != nil {
		return e
	}
	if t, ok := d["task"].(map[string]any); ok {
		d = t
	}
	state := strings.ToLower(str(d, "status"))
	statusText := map[string]string{"failed": "失败", "expired": "过期", "cancelled": "已取消", "canceled": "已取消", "queued": "排队中", "processing": "处理中", "pending": "排队中", "running": "生成中"}[state]
	if statusText == "" {
		statusText = state
	}
	switch state {
	case "failed", "expired", "canceled", "cancelled":
		_, e = a.db.Exec("UPDATE jobs SET state='failed',note=? WHERE id=?", "厂商任务状态："+statusText, id)
		return e
	case "success", "succeeded":
	default:
		_, e = a.db.Exec("UPDATE jobs SET note=? WHERE id=?", "厂商状态："+statusText, id)
		return e
	}
	ext, location := ".mp4", str(d, "content", "url")
	if p == "ark" {
		location = str(d, "content", "video_url")
	}
	if str(j, "kind") == "long" {
		fid := str(d, "file_id")
		f, e := a.mediaAPI(ctx, p, "GET", "/v1/files/retrieve?file_id="+url.QueryEscape(fid), nil)
		if e != nil {
			return e
		}
		location = str(f, "file", "download_url")
		ext = ".tar"
		if strings.HasSuffix(strings.ToLower(str(f, "file", "filename")), ".zip") {
			ext = ".zip"
		}
	}
	res, e := a.download(ctx, location)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	return a.storeAsset(id, ext, res.Body, usageInfo(d))
}
func (a *app) streamSpeech(w http.ResponseWriter, r *http.Request) (result error) {
	var p mediaInput
	if e := readBody(w, r, &p, 1<<20); e != nil {
		return e
	}
	p.Kind = "stream"
	if e := p.validate(); e != nil {
		return e
	}
	if a.providers["minimax"].key == "" {
		return bad("渠道尚未配置凭证")
	}
	id, e := a.newJob(p)
	if e != nil {
		return e
	}
	finished := false
	defer func() {
		if !finished {
			a.jobError(id, fault{502, "流式朗读已中断，计费用量待核实"})
		}
	}()
	data := speechPayload(p)
	data["stream"] = true
	data["stream_options"] = obj{"exclude_aggregated_audio": true}
	data["audio_setting"] = obj{"format": "pcm", "sample_rate": 24000, "channel": 1}
	b, _ := json.Marshal(data)
	res, e := a.upstream(r.Context(), "minimax", "POST", "/v1/t2a_v2", "application/json", bytes.NewReader(b))
	if e != nil {
		return fault{502, "连接语音服务失败"}
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return upstreamErr(res.StatusCode)
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	out := json.NewEncoder(w)
	fl, _ := w.(http.Flusher)
	s := bufio.NewScanner(res.Body)
	s.Buffer(make([]byte, 8192), 16<<20)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		d := obj{}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		if dec.Decode(&d) != nil {
			continue
		}
		if err := checkStatus(d); err != nil {
			out.Encode(obj{"error": err.Error()})
			return nil
		}
		if chunk := str(d, "data", "audio"); chunk != "" {
			raw, e := hex.DecodeString(chunk)
			if e != nil {
				break
			}
			if out.Encode(obj{"pcm": raw}) != nil {
				return nil
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if str(d, "data", "status") == "2" {
			_, e = a.db.Exec("UPDATE jobs SET state='done',usage=? WHERE id=?", usageInfo(d), id)
			if e != nil {
				return e
			}
			finished = true
			return out.Encode(obj{"done": true})
		}
	}
	fmt.Fprintln(w, `{"error":"语音流中断"}`)
	return nil
}
