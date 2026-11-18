import React, { useState, useEffect, useRef, useCallback } from "react";
import {
  Blank,
  Field,
  Notice,
  Status,
  api,
  mediaKinds,
  providers,
  when,
} from "./shared";
import { readLines } from "./playground";
function fileURL(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(r.result);
    r.onerror = () => reject(Error("文件读取失败"));
    r.readAsDataURL(file);
  });
}
const systemVoices = [
  ["male-qn-qingse", "青涩青年"],
  ["female-shaonv", "少女"],
  ["audiobook_male_1", "有声书男声"],
  ["audiobook_female_1", "有声书女声"],
];
export function Studio({ video }) {
  const [kind, setKind] = useState(video ? "video" : "speech"),
    [provider, setProvider] = useState("minimax"),
    [model, setModel] = useState("MiniMax-H3-Lite"),
    [prompt, setPrompt] = useState(""),
    [text, setText] = useState(""),
    [voice, setVoice] = useState(systemVoices[0][0]),
    [file, setFile] = useState(null),
    [consent, setConsent] = useState(false),
    [jobs, setJobs] = useState([]),
    [err, setErr] = useState(""),
    [busy, setBusy] = useState(false),
    [streaming, setStreaming] = useState(false);
  const player = useRef(null),
    mounted = useRef(true),
    seq = useRef(0);
  const refresh = useCallback(async () => {
    const n = ++seq.current;
    const d = await api("/api/jobs");
    if (mounted.current && n === seq.current) setJobs(d);
  }, []);
  function stop() {
    const p = player.current;
    if (p) {
      p.control.abort();
      p.ctx.close().catch(() => {});
      player.current = null;
      setStreaming(false);
    }
  }
  useEffect(() => {
    mounted.current = true;
    refresh().catch((e) => setErr(e.message));
    return () => {
      mounted.current = false;
      stop();
    };
  }, [refresh]);
  useEffect(() => {
    if (!jobs.some((j) => ["processing", "submitting"].includes(j.state)))
      return;
    const tick = setInterval(
      () => refresh().catch((e) => setErr(e.message)),
      5000,
    );
    return () => clearInterval(tick);
  }, [jobs, refresh]);
  async function stream() {
    const ctx = new AudioContext(),
      control = new AbortController();
    player.current = { ctx, control };
    setStreaming(true);
    try {
      await ctx.resume();
      const r = await fetch("/api/stream-speech", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text, voice }),
        signal: control.signal,
      });
      if (!r.ok) throw Error((await r.json()).error.message);
      let at = ctx.currentTime,
        done = false,
        carry = "";
      await readLines(r.body, (line) => {
        if (!line) return;
        const d = JSON.parse(line);
        if (d.error) throw Error(d.error);
        if (d.done) done = true;
        if (d.pcm) {
          const bytes = carry + atob(d.pcm),
            n = Math.floor(bytes.length / 2);
          carry = bytes.slice(n * 2);
          if (!n) return;
          const buf = ctx.createBuffer(1, n, 24000),
            out = buf.getChannelData(0);
          for (let i = 0; i < n; i++) {
            const word =
              bytes.charCodeAt(i * 2) | (bytes.charCodeAt(i * 2 + 1) << 8);
            out[i] = ((word << 16) >> 16) / 32768;
          }
          const source = ctx.createBufferSource();
          source.buffer = buf;
          source.connect(ctx.destination);
          at = Math.max(ctx.currentTime + 0.015, at);
          source.start(at);
          at += buf.duration;
        }
      });
      if (!done) throw Error("语音连接中断");
      await new Promise((resolve) => {
        const timer = setTimeout(
          resolve,
          Math.max(0, (at - ctx.currentTime) * 1000),
        );
        control.signal.addEventListener(
          "abort",
          () => {
            clearTimeout(timer);
            resolve();
          },
          { once: true },
        );
      });
    } finally {
      stop();
    }
  }
  async function submit() {
    setErr("");
    setBusy(true);
    try {
      if (kind === "stream") await stream();
      else {
        const p = {
          kind,
          text,
          prompt,
          voice,
          provider,
          ...(video ? { model } : {}),
        };
        if (file && video) {
          if (file.size > 12 * 1024 * 1024) throw Error("首帧图片需小于 12 MB");
          p.image = await fileURL(file);
        }
        if (kind === "clone") {
          if (!file || !consent) throw Error("请选择样音并确认授权");
          if (file.size > 20 * 1024 * 1024) throw Error("样音需小于 20 MB");
          p.sample = (await fileURL(file)).split(",")[1];
          p.filename = file.name;
          p.consent = consent;
        }
        await api("/api/jobs", p);
      }
    } catch (e) {
      if (e.name !== "AbortError") setErr(e.message);
    } finally {
      if (mounted.current) {
        setBusy(false);
        await refresh().catch((e) => setErr(e.message));
      }
    }
  }
  const voiceKinds = ["speech", "stream", "long"],
    voices = jobs.filter((j) => j.voice && j.state === "done");
  const list = jobs.filter((j) =>
    video ? j.kind === "video" : j.kind !== "video",
  );
  return (
    <>
      <div className="title">
        <div>
          <h1>{video ? "视频生成" : "声音工作室"}</h1>
        </div>
      </div>
      {!video && (
        <div className="tabs">
          {["speech", "stream", "long", "music", "clone", "design"].map((k) => (
            <button
              key={k}
              disabled={busy}
              className={kind === k ? "selected" : ""}
              onClick={() => {
                setKind(k);
                setFile(null);
                setConsent(false);
                setErr("");
              }}
            >
              {mediaKinds[k]}
            </button>
          ))}
        </div>
      )}
      <div className="studio">
        <section className="panel settings">
          <h2>{video ? "创建视频任务" : mediaKinds[kind]}</h2>
          <fieldset disabled={busy}>
            {video && (
              <>
                <Field title="视频渠道">
                  <select
                    value={provider}
                    onChange={(e) => {
                      setProvider(e.target.value);
                      setModel(
                        e.target.value === "ark"
                          ? "doubao-seedance-2-0-260128"
                          : "MiniMax-H3-Lite",
                      );
                    }}
                  >
                    <option value="minimax">MiniMax</option>
                    <option value="ark">火山方舟 · Seedance</option>
                  </select>
                </Field>
                <Field
                  title="模型"
                  help="生成 4 秒视频。默认分辨率 480P，MiniMax-H3 为 768P。"
                >
                  {provider === "ark" ? (
                    <input
                      value={model}
                      onChange={(e) => setModel(e.target.value)}
                    />
                  ) : (
                    <select
                      value={model}
                      onChange={(e) => setModel(e.target.value)}
                    >
                      {[
                        "MiniMax-H3-Lite",
                        "MiniMax-H3-Highspeed",
                        "MiniMax-H3",
                      ].map((m) => (
                        <option key={m}>{m}</option>
                      ))}
                    </select>
                  )}
                </Field>
              </>
            )}
            {(video || ["music", "design"].includes(kind)) && (
              <Field
                title={
                  video
                    ? "画面描述"
                    : kind === "music"
                      ? "音乐风格"
                      : "音色描述"
                }
              >
                <textarea
                  rows="3"
                  value={prompt}
                  onChange={(e) => setPrompt(e.target.value)}
                  placeholder={
                    video
                      ? "描述画面、主体与运动…"
                      : kind === "music"
                        ? "例如：轻快的木吉他民谣"
                        : "例如：清亮自然的普通话女声"
                  }
                />
              </Field>
            )}
            {voiceKinds.includes(kind) && (
              <Field
                title="朗读音色"
                help={
                  voices.some((j) => j.voice === voice)
                    ? "首次正式使用专属音色，厂商标价另收 ¥9.90。"
                    : ""
                }
              >
                <select
                  value={voice}
                  onChange={(e) => setVoice(e.target.value)}
                >
                  {systemVoices.map(([id, name]) => (
                    <option key={id} value={id}>
                      {name}
                    </option>
                  ))}
                  {voices.map((j) => (
                    <option key={j.id} value={j.voice}>
                      {mediaKinds[j.kind]} · {j.voice}
                    </option>
                  ))}
                </select>
              </Field>
            )}
            {!video && (
              <Field
                title={
                  kind === "music"
                    ? "歌词"
                    : ["clone", "design"].includes(kind)
                      ? "试听文案"
                      : "朗读文案"
                }
              >
                <textarea
                  rows={kind === "long" ? 9 : 4}
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                />
              </Field>
            )}
            {(video || kind === "clone") && (
              <Field
                title={video ? "首帧图片（可选）" : "授权样音"}
                help={
                  video
                    ? "选择首帧后使用图生视频。"
                    : "支持 MP3、M4A、WAV，建议 10 秒至 5 分钟。"
                }
              >
                <input
                  key={kind}
                  type="file"
                  accept={video ? "image/png,image/jpeg" : ".m4a,.mp3,.wav"}
                  onChange={(e) => setFile(e.target.files[0] || null)}
                />
              </Field>
            )}
            {kind === "clone" && (
              <label className="check">
                <input
                  type="checkbox"
                  checked={consent}
                  onChange={(e) => setConsent(e.target.checked)}
                />
                我有权将该样音上传至 MiniMax 用于复刻
              </label>
            )}
          </fieldset>
          <Notice error={err} />
          <div className="actions">
            {streaming ? (
              <button onClick={stop}>停止播放</button>
            ) : (
              <button
                className="primary"
                disabled={
                  busy ||
                  (video ? !prompt.trim() : !text.trim()) ||
                  (kind === "clone" && (!file || !consent))
                }
                onClick={submit}
              >
                {busy
                  ? "提交中…"
                  : kind === "stream"
                    ? "开始流式朗读"
                    : video || kind === "long"
                      ? "提交生成任务"
                      : "生成并试听"}
              </button>
            )}
          </div>
        </section>
        <section>
          <div className="sectionhead first">
            <h2>任务记录</h2>
            <button
              className="link"
              onClick={() => refresh().catch((e) => setErr(e.message))}
            >
              刷新 ↻
            </button>
          </div>
          {!list.length && (
            <div className="panel">
              <Blank>生成的任务与成品会显示在这里。</Blank>
            </div>
          )}
          {list.map((j) => (
            <div key={j.id} className="panel job">
              <div className="row split">
                <b>{mediaKinds[j.kind]}</b>
                <Status value={j.state} />
              </div>
              <small>
                {providers[j.provider] || "自托管"} · {j.model} ·{" "}
                {when(j.created)}
              </small>
              <code>{j.id}</code>
              {j.note && <p>{j.note}</p>}
              {j.asset && (
                <>
                  {j.kind === "video" ? (
                    <video
                      controls
                      preload="none"
                      src={"/api/assets/" + j.id}
                    />
                  ) : (
                    j.kind !== "long" && (
                      <audio
                        controls
                        preload="none"
                        src={"/api/assets/" + j.id}
                      />
                    )
                  )}
                  <div className="actions">
                    <a
                      href={"/api/assets/" + j.id + "?download=1"}
                      download
                      className="link"
                    >
                      下载{j.kind === "long" ? "配音成品包" : "成品"} ↓
                    </a>
                    {j.voice && (
                      <button
                        disabled={busy}
                        onClick={() => {
                          setKind("speech");
                          setVoice(j.voice);
                          setText("");
                          window.scrollTo(0, 0);
                        }}
                      >
                        使用此音色朗读
                      </button>
                    )}
                  </div>
                </>
              )}
              {j.remote && (
                <details>
                  <summary>厂商任务详情</summary>
                  <code>{j.remote}</code>
                  {j.usage && <code>{j.usage}</code>}
                </details>
              )}
            </div>
          ))}
        </section>
      </div>
    </>
  );
}
