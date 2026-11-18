package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

type keyConfig struct {
	id                    string
	allowed               []string
	budget, rpm, parallel int64
}

func (a *app) authorize(r *http.Request) (keyConfig, error) {
	var k keyConfig
	var allowed string
	var row *sql.Row
	if id := r.Header.Get("X-Console-Key"); id != "" && a.admin(r) {
		row = a.db.QueryRow("SELECT id,allowed,budget,rpm,parallel FROM keys WHERE id=? AND enabled=1", id)
	} else {
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || raw == "" {
			return k, fault{401, "请提供有效的网关 Key"}
		}
		row = a.db.QueryRow("SELECT id,allowed,budget,rpm,parallel FROM keys WHERE digest=? AND enabled=1", hash(raw))
	}
	if e := row.Scan(&k.id, &allowed, &k.budget, &k.rpm, &k.parallel); e != nil {
		return k, fault{401, "网关 Key 无效或已停用"}
	}
	return k, json.Unmarshal([]byte(allowed), &k.allowed)
}
func (k keyConfig) permits(m string) bool {
	return slices.Contains(k.allowed, "*") || slices.Contains(k.allowed, m)
}
func (a *app) available(w http.ResponseWriter, r *http.Request) error {
	k, e := a.authorize(r)
	if e != nil {
		return e
	}
	rr, e := rows(a.db, "SELECT alias AS id,kind,output_bound,temperature FROM models ORDER BY alias")
	if e != nil {
		return e
	}
	v := []obj{}
	for _, m := range rr {
		if k.permits(m["id"].(string)) {
			m["object"] = "model"
			v = append(v, m)
		}
	}
	return jsonOut(w, obj{"object": "list", "data": v})
}

type model struct {
	alias, provider, upstream, kind                            string
	inputBound, outputBound, inputRate, cachedRate, outputRate int64
	temp                                                       bool
}
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type payload struct {
	Model       string    `json:"model"`
	Messages    []message `json:"messages,omitempty"`
	Input       any       `json:"input,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Max         *int64    `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
	Options     *struct {
		Include bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
	Dimensions *int   `json:"dimensions,omitempty"`
	Encoding   string `json:"encoding_format,omitempty"`
}

func (m model) prepare(p *payload, kind string) (int64, error) {
	if m.kind != kind {
		return 0, bad("模型类型与接口不匹配")
	}
	batch := int64(1)
	maxOut := int64(0)
	if kind == "chat" {
		if len(p.Messages) == 0 || p.Input != nil || p.Dimensions != nil || p.Encoding != "" {
			return 0, bad("需要文本消息列表")
		}
		for _, msg := range p.Messages {
			if !slices.Contains([]string{"system", "user", "assistant"}, msg.Role) || msg.Content == "" {
				return 0, bad("消息角色或内容无效")
			}
		}
		if p.Temperature != nil && (!m.temp || *p.Temperature < 0 || *p.Temperature > 2) {
			return 0, bad("所选模型不支持该温度")
		}
		maxOut = min(int64(512), m.outputBound)
		if p.Max != nil {
			maxOut = *p.Max
		}
		if maxOut < 1 || maxOut > m.outputBound {
			return 0, bad("最大输出长度超过模型范围")
		}
		p.Max = &maxOut
		if p.Stream {
			p.Options = &struct {
				Include bool `json:"include_usage"`
			}{true}
		}
	} else {
		if p.Stream || len(p.Messages) > 0 || p.Max != nil || p.Temperature != nil || p.Options != nil {
			return 0, bad("向量接口不支持聊天或流式参数")
		}
		switch x := p.Input.(type) {
		case string:
			if x == "" {
				return 0, bad("输入不能为空")
			}
		case []any:
			if len(x) == 0 || len(x) > 128 {
				return 0, bad("每批需包含 1 至 128 条文本")
			}
			for _, s := range x {
				if t, ok := s.(string); !ok || t == "" {
					return 0, bad("仅支持文本向量输入")
				}
			}
			batch = int64(len(x))
		default:
			return 0, bad("请输入文本或文本列表")
		}
	}
	p.Model = m.upstream
	if m.inputBound == 0 || m.inputRate < 0 || (kind == "chat" && m.outputRate < 0) {
		return -1, nil
	}
	return batch*m.inputBound*max(m.inputRate, m.cachedRate) + maxOut*max(0, m.outputRate), nil
}
func (a *app) reserve(k keyConfig, m model, id string, hold, now int64) error {
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var budget, rpm, parallel, spent, held, active, recent int64
	var allowed string
	if e = tx.QueryRow("SELECT budget,rpm,parallel,allowed FROM keys WHERE id=? AND enabled=1", k.id).Scan(&budget, &rpm, &parallel, &allowed); e != nil {
		return fault{403, "Key 已停用"}
	}
	if e = json.Unmarshal([]byte(allowed), &k.allowed); e != nil {
		return e
	}
	if !k.permits(m.alias) {
		return fault{403, "Key 无权使用该模型"}
	}
	e = tx.QueryRow(`SELECT coalesce(sum(cost),0),coalesce(sum(hold),0),count(CASE WHEN state='running' THEN 1 END),count(CASE WHEN state!='rejected' AND created>? THEN 1 END) FROM calls WHERE key_id=?`, now-60000, k.id).Scan(&spent, &held, &active, &recent)
	if e != nil {
		return e
	}
	if parallel > 0 && active >= parallel {
		return fault{429, "已达到在途请求上限，请稍后重试"}
	}
	if rpm > 0 && recent >= rpm {
		return fault{429, "已达到每分钟请求上限，请稍后重试"}
	}
	if budget >= 0 {
		var unresolved int
		if e = tx.QueryRow("SELECT count(*) FROM calls WHERE key_id=? AND state IN ('running','pending') AND bounded=0 AND cost IS NULL", k.id).Scan(&unresolved); e != nil {
			return e
		}
		if unresolved > 0 {
			return fault{402, "先前请求尚无可核实的预留，请先核对账务"}
		}

		if hold < 0 {
			return fault{422, "该模型缺少已核实的费率或输入上界，无法预留额度"}
		}
		if budget-spent-held < hold {
			return fault{402, "可用额度不足，无法预留本次请求上限"}
		}
	}
	res, e := tx.Exec("INSERT INTO calls(id,created,key_id,model,provider,state,hold,bounded) VALUES(?,?,?,?,?,'running',?,?) ON CONFLICT(id) DO NOTHING", id, now, k.id, m.alias, m.provider, max(hold, 0), hold >= 0)
	if e != nil {
		return e
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fault{409, "此请求 ID 已使用，请查看原请求日志"}
	}
	return tx.Commit()
}

type usage struct {
	Input   *int64 `json:"prompt_tokens"`
	Output  *int64 `json:"completion_tokens"`
	Cached  *int64 `json:"prompt_cache_hit_tokens"`
	Details *struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}
type envelope struct {
	Usage *usage          `json:"usage"`
	ID    string          `json:"id"`
	Error json.RawMessage `json:"error"`
}

func usageValue(v *int64) bool { return v != nil && *v >= 0 && *v <= 1e9 }
func (m model) settle(u *usage) (input, output, cost any) {
	if u == nil || !usageValue(u.Input) {
		return
	}
	input = *u.Input
	out := int64(0)
	if m.kind == "chat" {
		if !usageValue(u.Output) {
			return
		}
		out = *u.Output
	}
	output = out
	if m.inputRate < 0 || (out > 0 && m.outputRate < 0) {
		return
	}
	cached := int64(0)
	cr := m.cachedRate
	if cr < 0 {
		cr = m.inputRate
	}
	if cr != m.inputRate {
		v := u.Cached
		if v == nil && u.Details != nil {
			v = u.Details.Cached
		}
		if !usageValue(v) || *v > *u.Input {
			return
		}
		cached = *v
	}
	cost = (*u.Input-cached)*m.inputRate + cached*cr + out*max(0, m.outputRate)
	return
}
func (a *app) relay(w http.ResponseWriter, r *http.Request) (result error) {
	id := "req_" + token(12)
	if proposed := r.Header.Get("X-Playground-Request-ID"); strings.HasPrefix(proposed, "req_") && len(proposed) == 28 && a.admin(r) {
		if _, err := hex.DecodeString(proposed[4:]); err == nil {
			id = proposed
		}
	}
	w.Header().Set("X-Request-ID", id)
	start := time.Now()
	k, e := a.authorize(r)
	if e != nil {
		return e
	}
	var p payload
	var m model
	reserved := false
	defer func() {
		if !reserved && result != nil {
			msg := "本地校验失败"
			var f fault
			if errors.As(result, &f) {
				msg = f.msg
			}
			_, err := a.db.Exec("INSERT INTO calls(id,created,key_id,model,provider,state,cost,note) VALUES(?,?,?,?,?,'rejected',0,?) ON CONFLICT(id) DO NOTHING", id, start.UnixMilli(), k.id, m.alias, m.provider, msg)
			if err != nil {
				result = err
			}
		}
	}()
	if e = readBody(w, r, &p, 2<<20); e != nil {
		return e
	}
	if !k.permits(p.Model) {
		return fault{403, "Key 无权使用该模型"}
	}
	e = a.db.QueryRow("SELECT alias,provider,upstream,kind,input_bound,output_bound,input_rate,cached_rate,output_rate,temperature FROM models WHERE alias=?", p.Model).Scan(&m.alias, &m.provider, &m.upstream, &m.kind, &m.inputBound, &m.outputBound, &m.inputRate, &m.cachedRate, &m.outputRate, &m.temp)
	if errors.Is(e, sql.ErrNoRows) {
		return bad("模型尚未配置")
	}
	if e != nil {
		return e
	}
	kind, path := "chat", "/chat/completions"
	if r.URL.Path == "/v1/embeddings" {
		kind, path = "embedding", "/embeddings"
	}
	hold, e := m.prepare(&p, kind)
	if e != nil {
		return e
	}
	if a.providers[m.provider].key == "" {
		return fault{503, "渠道尚未配置凭证"}
	}
	body, e := json.Marshal(p)
	if e != nil {
		return e
	}
	// Disable optional reasoning for text-only conversations. The output budget remains explicit.
	if m.provider == "deepseek" && kind == "chat" {
		var fields map[string]json.RawMessage
		json.Unmarshal(body, &fields)
		fields["thinking"] = json.RawMessage(`{"type":"disabled"}`)
		body, _ = json.Marshal(fields)
	}
	if r.Context().Err() != nil {
		return fault{408, "请求在发送前已取消"}
	}
	if e = a.reserve(k, m, id, hold, start.UnixMilli()); e != nil {
		return e
	}
	reserved = true
	var u *usage
	upID := ""
	note := "厂商未返回可核验用量或尚未配置适用费率"
	defer func() {
		in, out, cost := m.settle(u)
		var estimate any
		if cost == nil && u != nil {
			catalog := m
			switch {
			case m.provider == "deepseek" && (m.upstream == "deepseek-flash" || m.upstream == "deepseek-v4-flash"):
				catalog.inputRate, catalog.cachedRate, catalog.outputRate = 2000, 40, 8000
			case m.provider == "deepseek" && m.upstream == "deepseek-v4-pro":
				catalog.inputRate, catalog.cachedRate, catalog.outputRate = 9000, 300, 27000
			case m.provider == "bigmodel" && m.upstream == "embedding-3":
				catalog.inputRate, catalog.cachedRate, catalog.outputRate = 500, 500, 0
			}
			_, _, estimate = catalog.settle(u)
		}
		state := "pending"
		if cost != nil {
			state = "actual"
			if note == "厂商未返回可核验用量或尚未配置适用费率" {
				note = ""
			}
		}
		_, err := a.db.Exec(`UPDATE calls SET state=?,cost=?,estimate=?,hold=CASE WHEN ?='actual' THEN 0 ELSE hold END,input=?,output=?,duration=?,note=?,up_id=? WHERE id=? AND state='running'`, state, cost, estimate, state, in, out, time.Since(start).Milliseconds(), note, upID, id)
		if err != nil {
			result = err
		}
	}()
	res, e := a.upstream(r.Context(), m.provider, "POST", path, "application/json", bytes.NewReader(body))
	if e != nil {
		note = "上游超时、取消或连接失败，需核实费用"
		return fault{502, note}
	}
	defer res.Body.Close()
	upID = res.Header.Get("X-Request-ID")
	inspect := func(b []byte) bool {
		var v envelope
		if json.Unmarshal(b, &v) != nil {
			return false
		}
		if v.Usage != nil {
			u = v.Usage
		}
		if v.ID != "" {
			upID = v.ID
		}
		return len(v.Error) == 0 || string(v.Error) == "null"
	}
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		inspect(b)
		note = fmt.Sprintf("上游返回 HTTP %d", res.StatusCode)
		return upstreamErr(res.StatusCode)
	}
	if !p.Stream {
		b, e := io.ReadAll(io.LimitReader(res.Body, (32<<20)+1))
		if e != nil || len(b) > 32<<20 || !inspect(b) {
			note = "上游响应中断或格式错误"
			return fault{502, note}
		}
		w.Header().Set("Content-Type", "application/json")
		_, e = w.Write(b)
		return e
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)
	scan := bufio.NewScanner(res.Body)
	scan.Buffer(make([]byte, 32768), 4<<20)
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		done := data == "[DONE]"
		if !done && !inspect([]byte(data)) {
			break
		}
		if _, e = fmt.Fprintf(w, "data: %s\n\n", data); e != nil {
			note = "客户端已断开"
			return nil
		}
		if fl != nil {
			fl.Flush()
		}
		if done {
			return nil
		}
	}
	note = "流式响应中断，请按请求 ID 核对"
	fmt.Fprint(w, "data: {\"error\":{\"message\":\"上游流式连接中断\"}}\n\n")
	if fl != nil {
		fl.Flush()
	}
	return nil
}
