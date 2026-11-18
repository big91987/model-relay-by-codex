package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

const complete = `{"id":"receipt1","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"prompt_cache_hit_tokens":1}}`

type fixture struct {
	a      *app
	h      http.Handler
	cookie *http.Cookie
	secret string
}

func (f *fixture) do(ctx context.Context, path string, data any, key string, admin bool) *httptest.ResponseRecorder {
	method := "GET"
	text := ""
	if data != nil {
		method = "POST"
		b, _ := json.Marshal(data)
		text = string(b)
	}
	req := httptest.NewRequest(method, path, strings.NewReader(text)).WithContext(ctx)
	if admin && f.cookie != nil {
		req.AddCookie(f.cookie)
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, req)
	return w
}
func (f *fixture) post(t *testing.T, path string, p any) obj {
	t.Helper()
	w := f.do(context.Background(), path, p, "", true)
	if w.Code != 200 {
		t.Fatalf("%s %d %s", path, w.Code, w.Body)
	}
	v := obj{}
	json.Unmarshal(w.Body.Bytes(), &v)
	return v
}
func setup(t *testing.T, path string) *fixture {
	t.Helper()
	a, e := openApp(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(a.close)
	a.providers["deepseek"] = provider{"https://text.invalid", "fixture"}
	a.providers["bigmodel"] = provider{"https://embed.invalid", "fixture"}
	a.providers["minimax"] = provider{"https://media.invalid", "fixture"}
	f := &fixture{a: a, h: a.routes()}
	login := f.do(context.Background(), "/api/login", obj{"username": "admin", "password": env("ADMIN_PASSWORD", "admin")}, "", false)
	if len(login.Result().Cookies()) != 1 {
		t.Fatal("login")
	}
	f.cookie = login.Result().Cookies()[0]
	return f
}
func (f *fixture) model(t *testing.T, p, kind string) {
	f.post(t, "/api/models", obj{"alias": "test", "upstream": "up", "provider": p, "kind": kind, "inputBound": 10, "outputBound": 5, "inputRate": 10, "cachedRate": 2, "outputRate": 20, "temperature": true})
}
func (f *fixture) key(t *testing.T, budget, rpm, parallel int) string {
	return f.post(t, "/api/keys", obj{"name": "test", "allowed": []string{"test"}, "budget": budget, "rpm": rpm, "parallel": parallel, "enabled": true})["secret"].(string)
}
func chat(stream bool) obj {
	return obj{"model": "test", "messages": []obj{{"role": "user", "content": "hi"}}, "max_tokens": 5, "stream": stream}
}
func (f *fixture) call(key string, stream bool) *httptest.ResponseRecorder {
	return f.do(context.Background(), "/v1/chat/completions", chat(stream), key, false)
}
func (f *fixture) check(t *testing.T, id, state string, hold int64, cost any) {
	t.Helper()
	rr, e := rows(f.a.db, "SELECT state,hold,cost FROM calls WHERE id=?", id)
	if e != nil || len(rr) != 1 || rr[0]["state"] != state || rr[0]["hold"] != hold || rr[0]["cost"] != cost {
		t.Fatalf("账务 %s: %+v %v", id, rr, e)
	}
}
func TestConcurrentAdmission(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		budget, rpm, parallel, expected int
	}{{"余额", 200, 0, 0, 402}, {"并发", -1, 0, 1, 429}, {"RPM", -1, 1, 0, 429}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
			f.model(t, "deepseek", "chat")
			key := f.key(t, tc.budget, tc.rpm, tc.parallel)
			other := f.key(t, -1, 0, 0)
			entered, release := make(chan bool, 8), make(chan bool)
			f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
				entered <- true
				<-release
				return response(complete), nil
			})
			first := make(chan *httptest.ResponseRecorder, 1)
			go func() { first <- f.call(key, false) }()
			<-entered
			var wg sync.WaitGroup
			codes := make(chan int, 4)
			for range 4 {
				wg.Add(1)
				go func() { defer wg.Done(); codes <- f.call(key, false).Code }()
			}
			wg.Wait()
			close(codes)
			for c := range codes {
				if c != tc.expected {
					t.Fatalf("拒绝状态 %d", c)
				}
			}
			// Independent key reaches the provider while the original call is still in flight.
			independent := make(chan *httptest.ResponseRecorder, 1)
			go func() { independent <- f.call(other, false) }()
			<-entered
			close(release)
			for _, w := range []*httptest.ResponseRecorder{<-first, <-independent} {
				if w.Code != 200 {
					t.Fatal(w.Code)
				}
				f.check(t, w.Header().Get("X-Request-ID"), "actual", 0, int64(72))
			}
			if tc.name == "并发" && f.call(key, false).Code != 200 {
				t.Fatal("名额未释放")
			}
		})
	}
}
func TestConcurrentCompletionAndChannelChange(t *testing.T) {
	f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
	f.model(t, "deepseek", "chat")
	key := f.key(t, 1000, 0, 0)
	entered, release := make(chan bool, 5), make(chan bool)
	f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "text.invalid" {
			return nil, errors.New("意外切换")
		}
		entered <- true
		<-release
		return response(complete), nil
	})
	waits := make(chan int, 5)
	for range 5 {
		go func() { waits <- f.call(key, false).Code }()
	}
	for range 5 {
		<-entered
	}
	f.model(t, "bigmodel", "chat")
	close(release)
	for range 5 {
		if <-waits != 200 {
			t.Fatal("失败")
		}
	}
	var n, paid, held int
	if e := f.a.db.QueryRow("SELECT count(*),sum(cost),sum(hold) FROM calls WHERE provider='deepseek'").Scan(&n, &paid, &held); e != nil || n != 5 || paid != 360 || held != 0 {
		t.Fatalf("%d %d %d %v", n, paid, held, e)
	}
	f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "embed.invalid" {
			t.Error("新渠道未应用")
		}
		return response(complete), nil
	})
	if f.call(key, false).Code != 200 {
		t.Fatal("新渠道失败")
	}
}
func TestInterruptedAndUncertainCharges(t *testing.T) {
	f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
	f.model(t, "deepseek", "chat")
	key := f.key(t, 200, 0, 0)
	rejected := f.do(context.Background(), "/v1/chat/completions", obj{"model": "test", "messages": []message{}}, key, false)
	f.check(t, rejected.Header().Get("X-Request-ID"), "rejected", 0, int64(0))
	for _, tc := range []struct {
		name    string
		stream  bool
		payload string
		timeout bool
	}{{"缺少用量", false, `{"choices":[]}`, false}, {"流提前结束", true, "data: {\"choices\":[]}\n\n", false}, {"超时", false, "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			f.a.client.Transport = transport(func(*http.Request) (*http.Response, error) {
				if tc.timeout {
					return nil, context.DeadlineExceeded
				}
				return response(tc.payload), nil
			})
			w := f.call(key, tc.stream)
			id := w.Header().Get("X-Request-ID")
			f.check(t, id, "pending", 200, nil)
			f.post(t, "/api/reconcile", obj{"id": id, "cost": 0, "note": "测试上游确认未收费"})
			dup := f.do(context.Background(), "/api/reconcile", obj{"id": id, "cost": 1, "note": "重复通知"}, "", true)
			if dup.Code != 409 {
				t.Fatal(dup.Code)
			}
		})
	}
	f.a.client.Transport = transport(func(*http.Request) (*http.Response, error) { return response("data: " + complete + "\n\n"), nil })
	w := f.call(key, true)
	f.check(t, w.Header().Get("X-Request-ID"), "actual", 0, int64(72))
	if !strings.Contains(w.Body.String(), "中断") {
		t.Fatal("遗漏断流提示")
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan bool)
	f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	another := f.key(t, 200, 0, 0)
	ch := make(chan *httptest.ResponseRecorder)
	go func() { ch <- f.do(ctx, "/v1/chat/completions", chat(true), another, false) }()
	<-started
	cancel()
	w = <-ch
	f.check(t, w.Header().Get("X-Request-ID"), "pending", 200, nil)
}
func TestCrashRecovery(t *testing.T) {
	if path := os.Getenv("MODEL_GATE_CRASH_FIXTURE"); path != "" {
		f := setup(t, path)
		f.model(t, "deepseek", "chat")
		key := f.key(t, 200, 0, 0)
		f.a.client.Transport = transport(func(*http.Request) (*http.Response, error) { os.Exit(77); return nil, nil })
		f.call(key, true)
		return
	}
	path := filepath.Join(t.TempDir(), "gate.db")
	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecovery$")
	cmd.Env = append(os.Environ(), "MODEL_GATE_CRASH_FIXTURE="+path)
	e := cmd.Run()
	var ex *exec.ExitError
	if !errors.As(e, &ex) || ex.ExitCode() != 77 {
		t.Fatalf("子进程 %v", e)
	}
	f := setup(t, path)
	all := f.postList(t, "/api/calls")
	id := all["rows"].([]any)[0].(map[string]any)["id"].(string)
	f.check(t, id, "pending", 200, nil)
	f.post(t, "/api/reconcile", obj{"id": id, "cost": 43, "note": "上游测试账单流水 001"})
	f.check(t, id, "actual", 0, int64(43))
	if a, e := openApp(path); e == nil {
		a.close()
		t.Fatal("单实例锁失效")
	}
}
func (f *fixture) postList(t *testing.T, path string) obj {
	t.Helper()
	w := f.do(context.Background(), path, nil, "", true)
	v := obj{}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || w.Code != 200 {
		t.Fatalf("GET %s: %s", path, w.Body)
	}
	return v
}
func TestEmbeddingAndReports(t *testing.T) {
	f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
	f.model(t, "bigmodel", "embedding")
	key := f.key(t, -1, 0, 0)
	f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/embeddings" {
			t.Error(r.URL.Path)
		}
		return response(`{"data":[{"embedding":[0.1]}],"usage":{"prompt_tokens":4,"prompt_cache_hit_tokens":0}}`), nil
	})
	id := ""
	for range 23 {
		w := f.do(context.Background(), "/v1/embeddings", obj{"model": "test", "input": []string{"一", "二"}}, key, false)
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		id = w.Header().Get("X-Request-ID")
	}
	for _, page := range []int{1, 2} {
		d := f.postList(t, fmt.Sprintf("/api/calls?page=%d", page))
		if d["total"].(map[string]any)["cost"] != float64(920) || len(d["models"].([]any)) != 1 {
			t.Fatal(d["total"])
		}
	}
	if len(f.postList(t, "/api/calls?id="+id)["rows"].([]any)) != 1 {
		t.Fatal("精确查询")
	}
	csv := f.do(context.Background(), "/api/export", nil, "", true).Body.String()
	if strings.Count(csv, "\n") != 24 || strings.Contains(csv, key) {
		t.Fatal("导出明细错误")
	}
	if f.call("invalid", true).Code != 401 {
		t.Fatal("无效密钥")
	}
	if f.do(context.Background(), "/v1/embeddings", obj{"model": "other", "input": "x"}, key, false).Code != 403 {
		t.Fatal("模型权限失效")
	}
	if f.do(context.Background(), "/api/config", nil, "", false).Code != 401 {
		t.Fatal("控制台未鉴权")
	}
}
func TestMissingUpperBound(t *testing.T) {
	f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
	f.post(t, "/api/models", obj{"alias": "test", "upstream": "up", "provider": "deepseek", "kind": "chat", "inputBound": 0, "outputBound": 5, "inputRate": -1, "cachedRate": -1, "outputRate": -1})
	key := f.key(t, 1000, 0, 0)
	if f.call(key, false).Code != 422 {
		t.Fatal("未拒绝未知上界")
	}
	free := f.key(t, -1, 0, 0)
	f.a.client.Transport = transport(func(*http.Request) (*http.Response, error) { return response(complete), nil })
	w := f.call(free, false)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	f.check(t, w.Header().Get("X-Request-ID"), "pending", 0, nil)
}
func TestCustomVoiceAndMusic(t *testing.T) {
	var expectedVoice string
	f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
	f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/voice_design":
			return response(`{"voice_id":"designed","trial_audio":"49443301"}`), nil
		case "/v1/files/upload":
			return response(`{"file":{"file_id":123}}`), nil
		case "/v1/voice_clone":
			return response(`{"demo_audio":"49443301"}`), nil
		case "/v1/t2a_v2":
			var p obj
			json.NewDecoder(r.Body).Decode(&p)
			if str(p, "voice_setting", "voice_id") != expectedVoice || str(p, "text") != "另一段文案" {
				t.Error("未使用新音色和新文案")
			}
			return response(`{"data":{"audio":"49443301"}}`), nil
		case "/v1/music_generation":
			return response(`{"data":{"audio":"49443301"}}`), nil
		case "/v1/audio/speech":
			if r.Header.Get("Authorization") != "" {
				t.Error("凭证外传")
			}
			var p obj
			json.NewDecoder(r.Body).Decode(&p)
			if p["input"] != "歌词" || p["instructions"] != "民谣" {
				t.Error("音乐参数")
			}
			return response("RIFFxxxxWAVE"), nil
		}
		return nil, fmt.Errorf("未预期的路径 %s", r.URL.Path)
	})
	for _, kind := range []string{"design", "clone"} {
		id := f.post(t, "/api/jobs", obj{"kind": kind, "prompt": "描述", "text": "试听", "consent": true, "filename": "sample.m4a", "sample": "YXVkaW8="})["id"]
		var voice string
		f.a.db.QueryRow("SELECT voice FROM jobs WHERE id=?", id).Scan(&voice)
		if voice == "" {
			t.Fatal("没有音色 ID")
		}
		expectedVoice = voice
		f.post(t, "/api/jobs", obj{"kind": "speech", "voice": voice, "text": "另一段文案"})
	}
	for _, base := range []string{"", "http://music.invalid"} {
		t.Setenv("MUSIC3_BASE_URL", base)
		id := f.post(t, "/api/jobs", obj{"kind": "music", "prompt": "民谣", "text": "歌词"})["id"].(string)
		w := f.do(context.Background(), "/api/assets/"+id+"?download=1", nil, "", true)
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatal("下载成品失败")
		}
	}
}

func TestModelImportAndFavorite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gate.db")
	f := setup(t, path)
	f.model(t, "deepseek", "chat")
	f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) { return response(`{"data":[{"id":"new-model"}]}`), nil })
	d := f.do(context.Background(), "/api/discover?provider=deepseek", nil, "", true)
	if d.Code != 200 || !strings.Contains(d.Body.String(), "new-model") {
		t.Fatal(d.Body)
	}
	f.a.client.Transport = transport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	d = f.do(context.Background(), "/api/discover?provider=deepseek", nil, "", true)
	if d.Code != 502 {
		t.Fatal(d.Code)
	}
	f.post(t, "/api/favorite", obj{"alias": "test", "favorite": true})
	var fav int
	var up string
	if e := f.a.db.QueryRow("SELECT upstream,favorite FROM models WHERE alias='test'").Scan(&up, &fav); e != nil || up != "up" || fav != 1 {
		t.Fatal("配置或收藏丢失")
	}
}
func TestAsyncMediaRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gate.db")
	f := setup(t, path)
	f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/t2a_async_v2" {
			t.Error(r.URL.Path)
		}
		return response(`{"task_id":98765432101123456,"usage_characters":32}`), nil
	})
	id := f.post(t, "/api/jobs", obj{"kind": "long", "text": "长文本测试"})["id"].(string)
	f.a.close()
	recovered := setup(t, path)
	items, e := rows(recovered.a.db, "SELECT * FROM jobs WHERE state='processing'")
	if e != nil || len(items) != 1 || str(items[0], "remote") != "98765432101123456" {
		t.Fatalf("恢复任务失败 %v", e)
	}
	recovered.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/query/t2a_async_query_v2":
			return response(`{"status":"Success","file_id":123}`), nil
		case "/v1/files/retrieve":
			return response(`{"file":{"filename":"speech.zip","download_url":"https://cdn.invalid/result"}}`), nil
		case "/result":
			if r.Header.Get("Authorization") != "" {
				t.Error("下载携带了渠道密钥")
			}
			return response("PK-payload"), nil
		}
		t.Fatal("不应重新提交")
		return nil, nil
	})
	if e = recovered.a.poll(context.Background(), items[0]); e != nil {
		t.Fatal(e)
	}
	w := recovered.do(context.Background(), "/api/assets/"+id, nil, "", true)
	if w.Body.String() != "PK-payload" {
		t.Fatal(w.Body)
	}
	calls := recovered.postList(t, "/api/calls?id="+id)
	r := calls["rows"].([]any)[0].(map[string]any)
	if r["estimate"] != float64(6400000) || r["cost"] != nil {
		t.Fatal(r)
	}
}
func TestBillableUpstreamFailure(t *testing.T) {
	f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
	f.model(t, "deepseek", "chat")
	key := f.key(t, 200, 0, 0)
	f.a.client.Transport = transport(func(*http.Request) (*http.Response, error) {
		d := response(`{"error":{"message":"private upstream debug"},"usage":{"prompt_tokens":4,"completion_tokens":2,"prompt_cache_hit_tokens":1}}`)
		d.StatusCode = 500
		return d, nil
	})
	w := f.call(key, false)
	if w.Code != 502 || strings.Contains(w.Body.String(), "private") {
		t.Fatal("上游失败响应处理错误")
	}
	f.check(t, w.Header().Get("X-Request-ID"), "actual", 0, int64(72))
}

func TestPlaygroundRequestID(t *testing.T) {
	f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
	f.model(t, "deepseek", "chat")
	key := f.key(t, -1, 0, 0)
	hits := 0
	f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) { hits++; return response(complete), nil })
	id := "req_0123456789abcdef01234567"
	for _, expected := range []int{200, 409} {
		body, _ := json.Marshal(chat(false))
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(string(body)))
		req.AddCookie(f.cookie)
		req.Header.Set("X-Playground-Request-ID", id)
		req.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, req)
		if w.Code != expected || w.Header().Get("X-Request-ID") != id {
			t.Fatal(w.Code, w.Header())
		}
	}
	if hits != 1 {
		t.Fatal("重复调用")
	}
	f.check(t, id, "actual", 0, int64(72))
}

func TestPlaygroundParameterChanges(t *testing.T) {
	f := setup(t, filepath.Join(t.TempDir(), "gate.db"))
	f.model(t, "deepseek", "chat")
	key := f.key(t, -1, 0, 0)
	for _, temp := range []float64{0.3, 1.2} {
		maxTokens := int(temp*10)%5 + 1
		f.a.client.Transport = transport(func(r *http.Request) (*http.Response, error) {
			var p obj
			json.NewDecoder(r.Body).Decode(&p)
			if p["temperature"] != temp || p["max_tokens"] != float64(maxTokens) || p["model"] != "up" || field(p, "thinking", "type") != "disabled" {
				t.Errorf("参数不符 %+v", p)
			}
			if len(p["messages"].([]any)) != 3 {
				t.Error("上下文缺失")
			}
			if field(p, "stream_options", "include_usage") != true {
				t.Error("未请求流式用量")
			}
			return response("data: " + complete + "\n\ndata: [DONE]\n\n"), nil
		})
		body := obj{"model": "test", "messages": []message{{Role: "system", Content: "简洁"}, {Role: "user", Content: "颜色"}, {Role: "assistant", Content: "蓝色"}}, "temperature": temp, "max_tokens": maxTokens, "stream": true}
		w := f.do(context.Background(), "/v1/chat/completions", body, key, false)
		if w.Code != 200 {
			t.Fatal(w.Body)
		}
		f.check(t, w.Header().Get("X-Request-ID"), "actual", 0, int64(72))
	}
}
