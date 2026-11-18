package main

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func rows(q queryer, s string, args ...any) ([]obj, error) {
	r, e := q.Query(s, args...)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	cols, e := r.Columns()
	if e != nil {
		return nil, e
	}
	out := []obj{}
	for r.Next() {
		v := make([]any, len(cols))
		refs := make([]any, len(cols))
		for i := range v {
			refs[i] = &v[i]
		}
		if e = r.Scan(refs...); e != nil {
			return nil, e
		}
		m := obj{}
		for i, c := range cols {
			m[c] = v[i]
		}
		out = append(out, m)
	}
	return out, r.Err()
}
func (a *app) config(w http.ResponseWriter, r *http.Request) error {
	models, e := rows(a.db, "SELECT * FROM models ORDER BY provider,alias")
	if e != nil {
		return e
	}
	keys, e := rows(a.db, `SELECT k.id,k.name,k.allowed,k.budget,k.rpm,k.parallel,k.enabled,coalesce(sum(c.cost),0) spent,coalesce(sum(c.hold),0) held FROM keys k LEFT JOIN calls c ON c.key_id=k.id GROUP BY k.id ORDER BY k.rowid DESC`)
	if e != nil {
		return e
	}
	channels := obj{}
	for p, c := range a.providers {
		channels[p] = obj{"base": c.base, "ready": c.key != ""}
	}
	return jsonOut(w, obj{"models": models, "keys": keys, "channels": channels})
}
func (a *app) saveKey(w http.ResponseWriter, r *http.Request) error {
	var k struct {
		ID, Name              string
		Allowed               []string
		Budget, RPM, Parallel int64
		Enabled               bool
	}
	if e := readBody(w, r, &k, 32768); e != nil {
		return e
	}
	if strings.TrimSpace(k.Name) == "" || len(k.Name) > 120 || k.Budget < -1 || k.Budget > 9e15 || k.RPM < 0 || k.Parallel < 0 || len(k.Allowed) == 0 {
		return bad("请检查名称、允许的模型、额度和限流设置")
	}
	allowed, _ := json.Marshal(k.Allowed)
	secret := ""
	if k.ID == "" {
		k.ID = token(8)
		secret = "mg-" + token(24)
		_, e := a.db.Exec("INSERT INTO keys VALUES(?,?,?,?,?,?,?,?)", k.ID, k.Name, hash(secret), string(allowed), k.Budget, k.RPM, k.Parallel, k.Enabled)
		if e != nil {
			return e
		}
	} else {
		res, e := a.db.Exec("UPDATE keys SET name=?,allowed=?,budget=?,rpm=?,parallel=?,enabled=? WHERE id=?", k.Name, string(allowed), k.Budget, k.RPM, k.Parallel, k.Enabled, k.ID)
		if e != nil {
			return e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return bad("Key 不存在")
		}
	}
	return jsonOut(w, obj{"id": k.ID, "secret": secret})
}
func (a *app) saveModel(w http.ResponseWriter, r *http.Request) error {
	var p struct {
		Alias, Provider, Upstream, Kind                            string
		InputBound, OutputBound, InputRate, CachedRate, OutputRate int64
		Temperature                                                bool
	}
	p.InputRate, p.CachedRate, p.OutputRate = -1, -1, -1
	p.OutputBound = 4096
	if e := readBody(w, r, &p, 4096); e != nil {
		return e
	}
	if _, ok := a.providers[p.Provider]; !ok || !validName(p.Alias) || !validName(p.Upstream) || (p.Kind != "chat" && p.Kind != "embedding") {
		return bad("渠道、模型名称或类型无效")
	}
	for _, v := range []int64{p.InputRate, p.CachedRate, p.OutputRate} {
		if v < -1 || v > 1000000 {
			return bad("单价超出范围")
		}
	}
	if p.InputBound < 0 || p.InputBound > 4000000 || p.OutputBound < 1 || p.OutputBound > 1000000 {
		return bad("Token 上限超出范围")
	}
	_, e := a.db.Exec(`INSERT INTO models(alias,provider,upstream,kind,input_bound,output_bound,input_rate,cached_rate,output_rate,temperature) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(alias) DO UPDATE SET provider=excluded.provider,upstream=excluded.upstream,kind=excluded.kind,input_bound=excluded.input_bound,output_bound=excluded.output_bound,input_rate=excluded.input_rate,cached_rate=excluded.cached_rate,output_rate=excluded.output_rate,temperature=excluded.temperature`, p.Alias, p.Provider, p.Upstream, p.Kind, p.InputBound, p.OutputBound, p.InputRate, p.CachedRate, p.OutputRate, p.Temperature)
	if e != nil {
		return e
	}
	return jsonOut(w, obj{"ok": true})
}
func validName(s string) bool { return s != "" && len(s) <= 180 && !strings.ContainsAny(s, " \t\r\n") }
func (a *app) favorite(w http.ResponseWriter, r *http.Request) error {
	var p struct {
		Alias    string
		Favorite bool
	}
	if e := readBody(w, r, &p, 1024); e != nil {
		return e
	}
	_, e := a.db.Exec("UPDATE models SET favorite=? WHERE alias=?", p.Favorite, p.Alias)
	if e != nil {
		return e
	}
	return jsonOut(w, obj{"ok": true})
}
func (a *app) discover(w http.ResponseWriter, r *http.Request) error {
	p := r.URL.Query().Get("provider")
	res, e := a.upstream(r.Context(), p, "GET", "/models", "", nil)
	if e != nil {
		return fault{502, "无法获取渠道模型列表，请检查渠道配置"}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return upstreamErr(res.StatusCode)
	}
	var models struct{ Data []struct{ ID string } }
	if e = json.NewDecoder(res.Body).Decode(&models); e != nil || models.Data == nil {
		return fault{502, "渠道未提供兼容的模型列表"}
	}
	names := []string{}
	for _, v := range models.Data {
		if validName(v.ID) {
			names = append(names, v.ID)
		}
	}
	return jsonOut(w, names)
}
func (a *app) calls(w http.ResponseWriter, r *http.Request) error {
	p := r.URL.Query()
	from, to := int64(0), int64(1<<62)
	var e error
	if p.Has("from") {
		from, e = strconv.ParseInt(p.Get("from"), 10, 64)
		if e != nil {
			return bad("起始时间无效")
		}
	}
	if p.Has("to") {
		to, e = strconv.ParseInt(p.Get("to"), 10, 64)
		if e != nil {
			return bad("结束时间无效")
		}
	}
	if to <= from {
		return bad("时间范围无效")
	}
	where := " FROM calls WHERE created>=? AND created<?"
	args := []any{from, to}
	if id := p.Get("id"); id != "" {
		where += " AND id=?"
		args = append(args, id)
	}
	cols := "id,created,key_id,model,provider,state,input,output,cost,hold,duration,note,up_id,estimate"
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if r.URL.Path == "/api/export" {
		rr, e := tx.Query("SELECT "+cols+where+" ORDER BY created DESC,id", args...)
		if e != nil {
			return e
		}
		defer rr.Close()
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=calls.csv")
		fmt.Fprint(w, "\uFEFF")
		out := csv.NewWriter(w)
		out.Write([]string{"请求 ID", "提交时间 UTC", "Key ID", "模型", "渠道", "状态", "输入 Token", "输出 Token", "费用（元）", "占用（元）", "耗时毫秒", "备注", "厂商请求 ID", "估算费用（元）"})
		for rr.Next() {
			v := make([]any, 14)
			ref := make([]any, len(v))
			for i := range v {
				ref[i] = &v[i]
			}
			if e = rr.Scan(ref...); e != nil {
				return e
			}
			v[1] = time.UnixMilli(v[1].(int64)).UTC().Format(time.RFC3339Nano)
			for _, i := range []int{8, 9, 13} {
				if n, ok := v[i].(int64); ok {
					v[i] = fmt.Sprintf("%d.%09d", n/1e9, n%1e9)
				}
			}
			s := make([]string, len(v))
			for i, x := range v {
				if x != nil {
					s[i] = fmt.Sprint(x)
					if strings.IndexAny(strings.TrimSpace(s[i]), "=+-@") == 0 || strings.HasPrefix(s[i], "\t") {
						s[i] = "'" + s[i]
					}
				}
			}
			if e = out.Write(s); e != nil {
				return e
			}
		}
		out.Flush()
		if e = out.Error(); e != nil {
			return e
		}
		return rr.Err()
	}
	page, _ := strconv.Atoi(p.Get("page"))
	if page < 1 || page > 10000000 {
		page = 1
	}
	results, e := rows(tx, "SELECT "+cols+where+" ORDER BY created DESC,id LIMIT 20 OFFSET ?", append(append([]any{}, args...), (page-1)*20)...)
	if e != nil {
		return e
	}
	summary := "count(*) count,coalesce(sum(cost),0) cost,coalesce(sum(hold),0) held,count(CASE WHEN state='pending' THEN 1 END) pending"
	total, e := rows(tx, "SELECT "+summary+where, args...)
	if e != nil {
		return e
	}
	groups, e := rows(tx, "SELECT model,"+summary+where+" GROUP BY model ORDER BY cost DESC,model", args...)
	if e != nil {
		return e
	}
	return jsonOut(w, obj{"rows": results, "total": total[0], "models": groups, "page": page})
}
func (a *app) reconcile(w http.ResponseWriter, r *http.Request) error {
	var p struct {
		ID, Note string
		Cost     *int64
	}
	if e := readBody(w, r, &p, 4096); e != nil {
		return e
	}
	if p.Cost == nil || *p.Cost < 0 || *p.Cost > 9e15 || strings.TrimSpace(p.Note) == "" {
		return bad("需要账单金额和核实依据")
	}
	res, e := a.db.Exec("UPDATE calls SET state='actual',cost=?,hold=0,note=? WHERE id=? AND state='pending'", *p.Cost, p.Note, p.ID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fault{409, "记录已处理或尚未结束"}
	}
	return jsonOut(w, obj{"ok": true})
}
