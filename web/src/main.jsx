import React, { useState, useEffect, useCallback } from "react";
import { createRoot } from "react-dom/client";
import {
  api,
  providers,
  money,
  when,
  Status,
  Field,
  Notice,
  Blank,
} from "./shared";
import { Models, Keys, Reports } from "./manage";
import { Playground } from "./playground";
import { Studio } from "./studio";
import "./style.css";

const pages = [
  [
    "工作台",
    [
      ["overview", "网关概览", "▦"],
      ["models", "渠道与模型", "◇"],
      ["keys", "网关密钥", "⚿"],
    ],
  ],
  [
    "模型体验",
    [
      ["play", "聊天 Playground", "▤"],
      ["video", "视频生成", "▷"],
      ["audio", "声音工作室", "≋"],
    ],
  ],
  [
    "运营",
    [
      ["logs", "调用日志", "≡"],
      ["usage", "用量与费用", "◴"],
    ],
  ],
];
const pageInfo = Object.fromEntries(
  pages.flatMap(([group, items]) =>
    items.map(([id, title]) => [id, { group, title }]),
  ),
);
function Logo() {
  return (
    <span className="logo" aria-hidden="true">
      <i />
      <i />
      <i />
    </span>
  );
}
function Login({ done }) {
  const [err, setErr] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <main className="login">
      <section className="loginbox">
        <Logo />
        <h1>登录渡口</h1>
        <p>模型网关管理控制台</p>
        <form
          onSubmit={async (e) => {
            e.preventDefault();
            setBusy(true);
            setErr("");
            try {
              const v = new FormData(e.target);
              await api("/api/login", {
                username: v.get("user"),
                password: v.get("pass"),
              });
              await done();
            } catch (e) {
              setErr(e.message);
            } finally {
              setBusy(false);
            }
          }}
        >
          <Field title="管理员账号">
            <input
              name="user"
              autoComplete="username"
              required
              defaultValue="admin"
            />
          </Field>
          <Field title="密码">
            <input
              name="pass"
              type="password"
              autoComplete="current-password"
              required
              autoFocus
            />
          </Field>
          <Notice error={err} />
          <button className="primary" disabled={busy}>
            进入控制台　→
          </button>
        </form>
      </section>
    </main>
  );
}
function Overview({ config, go }) {
  const [report, setReport] = useState(null),
    [err, setErr] = useState("");
  useEffect(() => {
    const start = new Date();
    start.setHours(0, 0, 0, 0);
    api("/api/calls?from=" + start.getTime())
      .then(setReport)
      .catch((e) => setErr(e.message));
  }, []);
  const n = Object.values(config.channels).filter((c) => c.ready).length;
  return (
    <>
      <div className="title">
        <div>
          <h1>网关概览</h1>
          <p>查看渠道配置、今日调用与结算情况。</p>
        </div>
        <button className="primary" onClick={() => go("play")}>
          打开 Playground　↗
        </button>
      </div>
      <Notice error={err} />
      <div className="metrics">
        {[
          ["今日请求", report?.total.count ?? "—", "次"],
          ["已结算费用", report ? money(report.total.cost) : "—", ""],
          ["待核实记录", report?.total.pending ?? "—", "条"],
          ["启用密钥", config.keys.filter((k) => k.enabled).length, "把"],
        ].map(([k, v, u]) => (
          <div className="metric" key={k}>
            <span>{k}</span>
            <strong>
              {v}
              <small>{u}</small>
            </strong>
          </div>
        ))}
      </div>
      <div className="sectionhead">
        <h2>上游渠道</h2>
        <button className="link" onClick={() => go("models")}>
          管理模型 →
        </button>
      </div>
      <div className="channels">
        {Object.entries(providers).map(([k, v], i) => (
          <div className="channel" key={k}>
            <div className={"mark mark" + i}>{["D", "智", "M", "火"][i]}</div>
            <div>
              <b>{v}</b>
              <small>{new URL(config.channels[k].base).hostname}</small>
            </div>
            <span
              className={"dot " + (config.channels[k].ready ? "green" : "")}
              title={config.channels[k].ready ? "已配置凭证" : "未配置凭证"}
            />
          </div>
        ))}
      </div>
      <div className="footnote">{n} / 4 个渠道已配置服务端凭证</div>
      <div className="sectionhead">
        <h2>最近调用</h2>
        <button className="link" onClick={() => go("logs")}>
          全部日志 →
        </button>
      </div>
      <div className="panel">
        {report?.rows.length ? (
          <div className="tablewrap">
            <table>
              <thead>
                <tr>
                  <th>请求时间</th>
                  <th>模型</th>
                  <th>状态</th>
                  <th>费用</th>
                </tr>
              </thead>
              <tbody>
                {report.rows.slice(0, 5).map((r) => (
                  <tr key={r.id}>
                    <td>{when(r.created)}</td>
                    <td>{r.model || "—"}</td>
                    <td>
                      <Status value={r.state} />
                    </td>
                    <td>{money(r.cost)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ) : (
          <Blank>今日暂无调用记录</Blank>
        )}
      </div>
    </>
  );
}
function App() {
  const [cfg, setCfg] = useState(null),
    [ready, setReady] = useState(false),
    [err, setErr] = useState(""),
    [page, setPage] = useState(
      location.hash.slice(1).split("?")[0] || "overview",
    ),
    [open, setOpen] = useState(false),
    [theme, setTheme] = useState(
      localStorage.getItem("gate-theme") || "system",
    );
  const refresh = useCallback(async () => {
    const c = await api("/api/config");
    setCfg(c);
    return c;
  }, []);
  useEffect(() => {
    refresh()
      .catch((e) => {
        if (!e.message.includes("登录")) setErr(e.message);
      })
      .finally(() => setReady(true));
  }, [refresh]);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    localStorage.setItem("gate-theme", theme);
  }, [theme]);
  useEffect(() => {
    const cb = () => {
      setPage(location.hash.slice(1).split("?")[0] || "overview");
      setOpen(false);
    };
    window.addEventListener("hashchange", cb);
    return () => window.removeEventListener("hashchange", cb);
  }, []);
  const go = (id) => {
    location.hash = id;
  };
  if (!ready) return <div className="blank">正在载入工作区…</div>;
  if (!cfg)
    return (
      <>
        <Notice error={err} />
        <Login done={refresh} />
      </>
    );
  const props = { config: cfg, refresh, go },
    info = pageInfo[page] || pageInfo.overview;
  return (
    <div className="shell">
      {open && <div className="navcover" onClick={() => setOpen(false)} />}
      <aside className={open ? "opened" : ""}>
        <div className="brand">
          <Logo />
          <div>
            <b>渡口</b>
            <small>MODEL GATEWAY</small>
          </div>
        </div>
        <nav>
          {pages.map(([group, items]) => (
            <div key={group}>
              <label>{group}</label>
              {items.map(([id, title, icon]) => (
                <a
                  key={id}
                  href={"#" + id}
                  className={page === id ? "active" : ""}
                >
                  <span>{icon}</span>
                  {title}
                </a>
              ))}
            </div>
          ))}
        </nav>
        <div className="user">
          <b>A</b>
          <span>
            admin<small>工作区管理员</small>
          </span>
          <button
            title="退出登录"
            aria-label="退出登录"
            onClick={async () => {
              await api("/api/logout", {});
              setCfg(null);
            }}
          >
            ↪
          </button>
        </div>
      </aside>
      <main className="workspace">
        <header>
          <div>
            <button className="menu" onClick={() => setOpen(!open)}>
              ☰
            </button>
            工作区　/　{info.group}　/　<strong>{info.title}</strong>
          </div>
          <select
            aria-label="控制台主题"
            value={theme}
            onChange={(e) => setTheme(e.target.value)}
          >
            <option value="system">◐　跟随系统</option>
            <option value="light">☀　浅色</option>
            <option value="dark">☾　深色</option>
          </select>
        </header>
        <article key={page}>
          {page === "models" ? (
            <Models {...props} />
          ) : page === "keys" ? (
            <Keys {...props} />
          ) : page === "play" ? (
            <Playground {...props} />
          ) : ["logs", "usage"].includes(page) ? (
            <Reports mode={page} />
          ) : ["video", "audio"].includes(page) ? (
            <Studio video={page === "video"} />
          ) : (
            <Overview {...props} />
          )}
        </article>
      </main>
    </div>
  );
}
createRoot(document.getElementById("root")).render(<App />);
