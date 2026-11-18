import React, { useEffect, useState } from "react";
export async function api(url, body, options = {}) {
  const headers = {
    ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    ...options.headers,
  };
  const res = await fetch(url, {
    ...options,
    method: body === undefined ? "GET" : "POST",
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(
      (data.error?.message || "请求失败") +
        (data.request_id ? " · " + data.request_id : ""),
    );
  }
  return res.status === 204
    ? null
    : res.text().then((t) => (t ? JSON.parse(t) : null));
}
export const money = (n) =>
  n == null
    ? "待核实"
    : "¥" + (n / 1e9).toLocaleString("zh-CN", { maximumFractionDigits: 9 });
export const when = (t) =>
  new Date(t).toLocaleString("zh-CN", { hour12: false });
export const states = {
  actual: "已结算",
  pending: "待核实",
  running: "请求中",
  rejected: "已拒绝",
  processing: "生成中",
  submitting: "提交中",
  unknown: "待核实",
  failed: "失败",
  done: "已完成",
};
export const providers = {
  deepseek: "DeepSeek",
  bigmodel: "智谱",
  minimax: "MiniMax",
  ark: "火山方舟",
};
export const mediaKinds = {
  video: "视频生成",
  speech: "文本朗读",
  stream: "流式朗读",
  long: "长文本配音",
  music: "音乐生成",
  design: "音色设计",
  clone: "音色复刻",
};
export const unit = 1e9;
export function Status({ value }) {
  return (
    <span className={"status " + value}>
      <i />
      {states[value] || value}
    </span>
  );
}
export function Blank({ children }) {
  return <div className="blank">{children}</div>;
}
export function Field({ title, children, help }) {
  return (
    <label className="field">
      <span>{title}</span>
      {children}
      {help && <small>{help}</small>}
    </label>
  );
}
export function Modal({ title, close, children }) {
  useEffect(() => {
    const listener = (e) => {
      if (e.key === "Escape") close();
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [close]);
  return (
    <div className="veil">
      <section
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className="modal"
      >
        <div className="row split">
          <h2>{title}</h2>
          <button onClick={close} aria-label="关闭">
            ×
          </button>
        </div>
        {children}
      </section>
    </div>
  );
}
export function Action({ action, children, ...props }) {
  const [busy, setBusy] = useState(false),
    [err, setErr] = useState("");
  return (
    <>
      <button
        {...props}
        disabled={props.disabled || busy}
        onClick={async () => {
          setBusy(true);
          setErr("");
          try {
            await action();
          } catch (e) {
            setErr(e.message);
          } finally {
            setBusy(false);
          }
        }}
      >
        {busy ? "处理中…" : children}
      </button>
      {err && (
        <span className="error" role="alert">
          {err}
        </span>
      )}
    </>
  );
}
export function Notice({ error }) {
  return error ? (
    <p role="alert" className="error">
      {error}
    </p>
  ) : null;
}
export function download(text, name, type) {
  const link = document.createElement("a");
  link.href = URL.createObjectURL(new Blob([text], { type }));
  link.download = name;
  link.click();
  setTimeout(() => URL.revokeObjectURL(link.href), 5000);
}
