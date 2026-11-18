package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	_ "modernc.org/sqlite"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed migrations/*.sql
var migrations embed.FS

type obj = map[string]any
type fault struct {
	status int
	msg    string
}

func (e fault) Error() string { return e.msg }
func bad(s string) error      { return fault{400, s} }
func token(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func hash(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }
func env(k, d string) string {
	if s := os.Getenv(k); s != "" {
		return s
	}
	return d
}
func loadPrivate(path string) error {
	f, e := os.Open(path)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(s.Text()), "=")
		if ok {
			switch k {
			case "DEEPSEEK_API_KEY", "BIGMODEL_API_KEY", "MINIMAX_API_KEY", "ARK_API_KEY":
				if os.Getenv(k) == "" {
					os.Setenv(k, strings.Trim(v, " \"'"))
				}
			}
		}
	}
	return s.Err()
}

type provider struct{ base, key string }
type app struct {
	db        *sql.DB
	providers map[string]provider
	client    *http.Client
	dir       string
	lock      *os.File
}

func openApp(path string) (*app, error) {
	dir := filepath.Dir(path)
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		lock.Close()
		return nil, errors.New("数据库正由其他实例使用")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		lock.Close()
		return nil, e
	}
	f.Close()
	db, e := sql.Open("sqlite", path)
	if e != nil {
		lock.Close()
		return nil, e
	}
	db.SetMaxOpenConns(1)
	a := &app{db: db, dir: path + ".assets", lock: lock, providers: map[string]provider{
		"deepseek": {"https://api.deepseek.com/v1", os.Getenv("DEEPSEEK_API_KEY")},
		"bigmodel": {"https://open.bigmodel.cn/api/paas/v4", os.Getenv("BIGMODEL_API_KEY")},
		"minimax":  {"https://api.minimaxi.com", os.Getenv("MINIMAX_API_KEY")},
		"ark":      {"https://ark.cn-beijing.volces.com/api/v3", os.Getenv("ARK_API_KEY")},
	}, client: &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}}
	if _, e = db.Exec("PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL; PRAGMA foreign_keys=ON"); e == nil {
		e = a.migrate()
	}
	if e != nil {
		a.close()
		return nil, e
	}
	return a, nil
}
func (a *app) close() { a.db.Close(); a.lock.Close() }
func (a *app) migrate() error {
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var v int
	if e = tx.QueryRow("PRAGMA user_version").Scan(&v); e != nil {
		return e
	}
	scripts, e := migrations.ReadDir("migrations")
	if e != nil {
		return e
	}
	if v > len(scripts) {
		return errors.New("数据库版本高于程序版本")
	}
	for _, entry := range scripts[v:] {
		sqlText, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(sqlText)); err != nil {
			return err
		}
	}

	_, e = tx.Exec("UPDATE calls SET state='pending', note='网关重启，请按厂商账单核实' WHERE state='running' AND key_id!='console'; UPDATE jobs SET state='unknown',note='提交中断，请在厂商后台核实' WHERE state='submitting'")
	if e != nil {
		return e
	}
	return tx.Commit()
}
func jsonOut(w http.ResponseWriter, v any) error {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	return json.NewEncoder(w).Encode(v)
}
func readBody(w http.ResponseWriter, r *http.Request, v any, n int64) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, n))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return bad("请求格式不正确或内容过大")
	}
	return nil
}

type handler func(http.ResponseWriter, *http.Request) error

func handle(h handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if e := h(w, r); e != nil {
			status, msg := 500, "服务暂不可用"
			var f fault
			if errors.As(e, &f) {
				status, msg = f.status, f.msg
			} else {
				log.Printf("处理失败 %T", e)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			jsonOut(w, obj{"error": obj{"message": msg}, "request_id": w.Header().Get("X-Request-ID")})
		}
	}
}
func (a *app) admin(r *http.Request) bool {
	c, e := r.Cookie("mg_session")
	if e != nil {
		return false
	}
	var n int
	return a.db.QueryRow("SELECT 1 FROM sessions WHERE digest=? AND expires>?", hash(c.Value), time.Now().Unix()).Scan(&n) == nil
}
func (a *app) routes() http.Handler {
	m := http.NewServeMux()
	reg := func(path string, h handler) { m.HandleFunc(path, handle(h)) }
	private := func(path string, h handler) {
		reg(path, func(w http.ResponseWriter, r *http.Request) error {
			if !a.admin(r) {
				return fault{401, "请先登录控制台"}
			}
			return h(w, r)
		})
	}
	reg("POST /api/login", func(w http.ResponseWriter, r *http.Request) error {
		var p struct{ Username, Password string }
		if e := readBody(w, r, &p, 4096); e != nil {
			return e
		}
		if p.Username != "admin" || subtle.ConstantTimeCompare([]byte(p.Password), []byte(env("ADMIN_PASSWORD", "admin"))) != 1 {
			return fault{401, "用户名或密码错误"}
		}
		s := token(32)
		_, e := a.db.Exec("DELETE FROM sessions WHERE expires<=?", time.Now().Unix())
		if e != nil {
			return e
		}
		_, e = a.db.Exec("INSERT INTO sessions VALUES(?,?)", hash(s), time.Now().Add(8*time.Hour).Unix())
		if e != nil {
			return e
		}
		http.SetCookie(w, &http.Cookie{Name: "mg_session", Value: s, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil})
		return jsonOut(w, obj{"ok": true})
	})
	private("POST /api/logout", func(w http.ResponseWriter, r *http.Request) error {
		c, _ := r.Cookie("mg_session")
		_, e := a.db.Exec("DELETE FROM sessions WHERE digest=?", hash(c.Value))
		http.SetCookie(w, &http.Cookie{Name: "mg_session", Path: "/", MaxAge: -1, HttpOnly: true})
		return e
	})
	private("GET /api/config", a.config)
	private("POST /api/keys", a.saveKey)
	private("POST /api/models", a.saveModel)
	private("POST /api/favorite", a.favorite)
	private("GET /api/discover", a.discover)
	private("GET /api/calls", a.calls)
	private("GET /api/export", a.calls)
	private("POST /api/reconcile", a.reconcile)
	reg("GET /v1/models", a.available)
	reg("POST /v1/chat/completions", a.relay)
	reg("POST /v1/embeddings", a.relay)
	private("GET /api/jobs", a.jobs)
	private("POST /api/jobs", a.createJob)
	private("POST /api/stream-speech", a.streamSpeech)
	private("GET /api/assets/{id}", a.asset)
	m.Handle("GET /", http.FileServer(http.Dir("web/dist")))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		if r.Method == "POST" && r.Header.Get("Origin") != "" {
			u, e := url.Parse(r.Header.Get("Origin"))
			if e != nil || u.Host != r.Host {
				http.Error(w, "来源不匹配", 403)
				return
			}
		}
		m.ServeHTTP(w, r)
	})
}
func main() {
	if e := loadPrivate(env("PROVIDERS_ENV", "local/providers.env")); e != nil {
		log.Fatal("读取私有配置失败")
	}
	a, e := openApp(env("SQLITE_PATH", "data/exp_0042.sqlite"))
	if e != nil {
		log.Fatal(e)
	}
	defer a.close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	addr := env("LISTEN_ADDR", "127.0.0.1:8787")
	ln, e := net.Listen("tcp", addr)
	if e != nil {
		log.Fatal(e)
	}
	workerDone := make(chan struct{})
	go func() { defer close(workerDone); a.worker(ctx) }()
	server := &http.Server{Handler: a.routes(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		grace, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if e := server.Shutdown(grace); e != nil {
			server.Close()
		}
	}()
	log.Printf("网关已就绪 http://%s", ln.Addr())
	if e = server.Serve(ln); e != nil && !errors.Is(e, http.ErrServerClosed) {
		log.Printf("监听结束 %v", e)
	}
	stop()
	<-shutdownDone
	<-workerDone
}
func (a *app) upstream(ctx context.Context, p, method, path, content string, body io.Reader) (*http.Response, error) {
	c, ok := a.providers[p]
	if !ok || c.key == "" {
		return nil, fault{503, "渠道尚未配置凭证"}
	}
	req, e := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if content != "" {
		req.Header.Set("Content-Type", content)
	}
	return a.client.Do(req)
}
func upstreamErr(status int) error {
	return fault{502, fmt.Sprintf("上游返回 HTTP %d，请核对模型、账号权限和余额", status)}
}
