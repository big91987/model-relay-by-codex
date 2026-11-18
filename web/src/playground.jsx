import React, { useState, useEffect, useRef } from "react";
import { Field, Modal, Notice, api, download } from "./shared";
export async function readLines(stream, fn) {
  if (!stream) throw Error("浏览器未收到响应流");
  const reader = stream.getReader(),
    decoder = new TextDecoder();
  let buf = "";
  try {
    while (true) {
      const { done, value } = await reader.read();
      buf += done ? decoder.decode() : decoder.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf("\n")) >= 0) {
        fn(buf.slice(0, i).trimEnd());
        buf = buf.slice(i + 1);
      }
      if (done) {
        if (buf) fn(buf);
        break;
      }
    }
  } finally {
    reader.releaseLock();
  }
}
export function Playground({ config }) {
  const [key, setKey] = useState(
      config.keys.find((k) => k.enabled)?.id || "manual",
    ),
    [raw, setRaw] = useState(""),
    [loaded, setLoaded] = useState(""),
    [models, setModels] = useState([]),
    [model, setModel] = useState(""),
    [system, setSystem] = useState(""),
    [temp, setTemp] = useState("0.7"),
    [max, setMax] = useState(256),
    [msgs, setMsgs] = useState([]),
    [draft, setDraft] = useState(""),
    [err, setErr] = useState(""),
    [busy, setBusy] = useState(false),
    [meta, setMeta] = useState(null),
    [confirmRetry, setConfirmRetry] = useState(false);
  const task = useRef(null),
    scroller = useRef(null);
  const manual = key === "manual";
  const credential = manual ? loaded : key;
  const headers = manual
    ? { Authorization: "Bearer " + credential }
    : { "X-Console-Key": credential };
  const current = models.find((m) => m.id === model);
  useEffect(() => {
    let active = true;
    setModels([]);
    setModel("");
    setErr("");
    if (credential)
      api("/v1/models", undefined, { headers })
        .then((d) => {
          if (!active) return;
          const list = d.data.filter((m) => m.kind === "chat");
          setModels(list);
          if (list.length) {
            setModel(list[0].id);
            setMax((x) => Math.min(x, list[0].output_bound));
          } else setErr("此 Key 没有可用的聊天模型。");
        })
        .catch((e) => {
          if (active) setErr(e.message);
        });
    return () => {
      active = false;
    };
  }, [key, credential]);
  useEffect(() => () => task.current?.abort(), []);
  useEffect(() => {
    if (scroller.current)
      scroller.current.scrollTop = scroller.current.scrollHeight;
  }, [msgs]);
  async function send(retry = false) {
    if (task.current || !current || (!retry && !draft.trim())) return;
    const history = retry
      ? msgs.slice(0, msgs.map((m) => m.role).lastIndexOf("user") + 1)
      : [
          ...msgs.filter((m) => m.content),
          { role: "user", content: draft.trim() },
        ];
    if (!history.length) return;
    const controller = new AbortController();
    task.current = controller;
    const t = performance.now();
    let id =
        "req_" +
        Array.from(window.crypto.getRandomValues(new Uint8Array(12)), (n) =>
          n.toString(16).padStart(2, "0"),
        ).join(""),
      usage = null,
      reply = "",
      ended = false;
    setMsgs([...history, { role: "assistant", content: "" }]);
    if (!retry) setDraft("");
    setBusy(true);
    setErr("");
    setMeta(null);
    try {
      const body = {
        model,
        messages: [
          ...(system ? [{ role: "system", content: system }] : []),
          ...history,
        ],
        stream: true,
        max_tokens: Number(max),
        ...(current.temperature ? { temperature: Number(temp) } : {}),
      };
      const r = await fetch("/v1/chat/completions", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Playground-Request-ID": id,
          ...headers,
        },
        body: JSON.stringify(body),
        signal: controller.signal,
      });
      id = r.headers.get("X-Request-ID") || id;
      if (!r.ok) throw Error((await r.json()).error?.message || "网关请求失败");
      await readLines(r.body, (line) => {
        if (!line.startsWith("data:")) return;
        const data = line.slice(5).trim();
        if (data === "[DONE]") {
          ended = true;
          return;
        }
        const v = JSON.parse(data);
        if (v.error) throw Error(v.error.message);
        if (v.usage) usage = v.usage;
        const next = v.choices?.[0]?.delta?.content;
        if (next) {
          reply += next;
          setMsgs([...history, { role: "assistant", content: reply }]);
        }
      });
      if (!ended) throw Error("连接已中断，回复可能不完整。");
    } catch (e) {
      controller.abort();
      setErr(
        e.name === "AbortError"
          ? "已停止生成，已产生的用量仍可能计费。"
          : e.message,
      );
      if (!reply) setMsgs(history);
    } finally {
      setMeta({ id, usage, ms: Math.round(performance.now() - t) });
      task.current = null;
      setBusy(false);
    }
  }
  function reset() {
    setMsgs([]);
    setMeta(null);
    setErr("");
  }
  const markdown = () =>
    download(
      msgs
        .filter((m) => m.content)
        .map(
          (m) =>
            "## " + (m.role === "user" ? "用户" : "助手") + "\n\n" + m.content,
        )
        .join("\n\n") + "\n",
      "当前对话.md",
      "text/markdown;charset=utf-8",
    );
  return (
    <>
      <div className="title">
        <div>
          <h1>聊天 Playground</h1>
        </div>
        <div className="actions">
          <button disabled={busy || !msgs.length} onClick={markdown}>
            ↓ 导出对话
          </button>
          <button disabled={busy || !msgs.length} onClick={reset}>
            清空对话
          </button>
        </div>
      </div>
      <div className="playlayout">
        <section className="panel conversation">
          <div className="sectionhead">
            <h2>当前对话</h2>
            <code>{model || "未选择模型"}</code>
          </div>
          <div className="messages" ref={scroller}>
            {!msgs.length ? (
              <div className="welcome">
                <span>✳</span>
                <h2>开始一段对话</h2>
                <p>选择密钥和模型，发送第一条消息。</p>
              </div>
            ) : (
              msgs.map((m, i) => (
                <div className={"message " + m.role} key={i}>
                  <small>{m.role === "user" ? "你" : "模型回复"}</small>
                  <div>{m.content || "正在生成…"}</div>
                </div>
              ))
            )}
          </div>
          <Notice error={err} />
          {meta && (
            <div className="receipt">
              {meta.ms} ms　·　
              {meta.usage
                ? (meta.usage.total_tokens ??
                    (meta.usage.prompt_tokens || 0) +
                      (meta.usage.completion_tokens || 0)) + " Token"
                : "用量不可用"}
              　
              {meta.id && (
                <a href={"#logs?id=" + encodeURIComponent(meta.id)}>
                  {meta.id} ↗
                </a>
              )}
            </div>
          )}
          <div className="composer">
            <textarea
              placeholder="输入消息…"
              aria-label="聊天消息"
              rows="3"
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if ((e.ctrlKey || e.metaKey) && e.key === "Enter") send();
              }}
            />
            <div className="row split">
              <button
                className="link"
                disabled={busy || !msgs.length || !model}
                onClick={() => setConfirmRetry(true)}
              >
                重新发送
              </button>
              {busy ? (
                <button onClick={() => task.current?.abort()}>停止生成</button>
              ) : (
                <button
                  className="primary"
                  disabled={!draft.trim() || !model}
                  onClick={() => send()}
                >
                  发送 ↑
                </button>
              )}
            </div>
          </div>
        </section>
        <section className="panel settings">
          <h2>请求设置</h2>
          <fieldset disabled={busy}>
            <Field title="网关密钥">
              <select
                value={key}
                onChange={(e) => {
                  setKey(e.target.value);
                  setLoaded("");
                  setRaw("");
                  reset();
                }}
              >
                {config.keys
                  .filter((k) => k.enabled)
                  .map((k) => (
                    <option key={k.id} value={k.id}>
                      {k.name}
                    </option>
                  ))}
                <option value="manual">手动输入 Key</option>
              </select>
            </Field>
            {manual && (
              <Field title="当前页面密钥">
                <input
                  autoComplete="off"
                  type="password"
                  value={raw}
                  onChange={(e) => {
                    setRaw(e.target.value);
                    setLoaded("");
                  }}
                />
                <button
                  disabled={!raw.trim()}
                  onClick={() => setLoaded(raw.trim())}
                >
                  载入可用模型
                </button>
              </Field>
            )}
            <Field
              title="聊天模型"
              help="切换模型会保留当前上下文。切换密钥会清空对话。"
            >
              <select
                value={model}
                onChange={(e) => {
                  setModel(e.target.value);
                  setMax(
                    Math.min(
                      max,
                      models.find((m) => m.id === e.target.value).output_bound,
                    ),
                  );
                }}
              >
                {!model && <option>暂无模型</option>}
                {models.map((m) => (
                  <option key={m.id}>{m.id}</option>
                ))}
              </select>
            </Field>
            <Field title="系统提示词">
              <textarea
                rows="3"
                value={system}
                placeholder="可选"
                onChange={(e) => setSystem(e.target.value)}
              />
            </Field>
            <Field
              title="温度"
              help={
                current && !current.temperature
                  ? "该模型配置未启用温度参数。"
                  : ""
              }
            >
              <input
                type="number"
                disabled={!current?.temperature}
                min="0"
                max="2"
                step=".1"
                value={temp}
                onChange={(e) => setTemp(e.target.value)}
              />
            </Field>
            <Field
              title="最大输出 Token"
              help={current ? "上限 " + current.output_bound : ""}
            >
              <input
                type="number"
                min="1"
                max={current?.output_bound}
                value={max}
                onChange={(e) => setMax(e.target.value)}
              />
            </Field>
          </fieldset>
        </section>
      </div>
      {confirmRetry && (
        <Modal title="重新发送上一条消息" close={() => setConfirmRetry(false)}>
          <p>将使用当前参数发起新的请求，可能产生新费用。</p>
          <div className="actions">
            <button onClick={() => setConfirmRetry(false)}>取消</button>
            <button
              className="primary"
              onClick={() => {
                setConfirmRetry(false);
                send(true);
              }}
            >
              确认重新发送
            </button>
          </div>
        </Modal>
      )}
    </>
  );
}
