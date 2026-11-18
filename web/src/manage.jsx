import React, { useEffect, useState } from "react";
import {
  Action,
  Blank,
  Field,
  Modal,
  Notice,
  Status,
  api,
  money,
  providers,
  unit,
  when,
} from "./shared";
const initialModel = {
  alias: "",
  provider: "deepseek",
  upstream: "",
  kind: "chat",
  input_bound: 0,
  output_bound: 4096,
  input_rate: -1,
  cached_rate: -1,
  output_rate: -1,
  temperature: 1,
};
function modelPayload(m) {
  return {
    alias: m.alias.trim(),
    provider: m.provider,
    upstream: m.upstream.trim(),
    kind: m.kind,
    inputBound: Number(m.input_bound),
    outputBound: Number(m.output_bound),
    inputRate: m.input_rate,
    cachedRate: m.cached_rate,
    outputRate: m.output_rate,
    temperature: !!m.temperature,
  };
}
function ModelEditor({ model, close, refresh }) {
  const [m, setM] = useState({ ...model }),
    set = (k, v) => setM((s) => ({ ...s, [k]: v }));
  return (
    <Modal title={model.alias ? "编辑模型" : "手动添加模型"} close={close}>
      <div className="formgrid">
        <Field title="上游渠道">
          <select
            value={m.provider}
            onChange={(e) => set("provider", e.target.value)}
          >
            {Object.entries(providers).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </Field>
        <Field title="接口类型">
          <select value={m.kind} onChange={(e) => set("kind", e.target.value)}>
            <option value="chat">聊天</option>
            <option value="embedding">文本向量</option>
          </select>
        </Field>
        <Field title="上游模型 ID">
          <input
            value={m.upstream}
            onChange={(e) => set("upstream", e.target.value)}
          />
        </Field>
        <Field title="网关别名">
          <input
            value={m.alias}
            disabled={!!model.alias}
            onChange={(e) => set("alias", e.target.value)}
          />
        </Field>
        <Field
          title="厂商每条输入的硬上界（Token）"
          help="用于有限额度预留。未知填 0。"
        >
          <input
            type="number"
            value={m.input_bound}
            onChange={(e) => set("input_bound", e.target.value)}
          />
        </Field>
        <Field title="最大输出 Token">
          <input
            type="number"
            value={m.output_bound}
            onChange={(e) => set("output_bound", e.target.value)}
          />
        </Field>
        {["input_rate", "cached_rate", "output_rate"].map((k, i) => (
          <Field
            key={k}
            title={
              ["输入单价", "缓存命中单价", "输出单价"][i] +
              "（元 / 百万 Token）"
            }
            help={
              i === 1
                ? "留空沿用输入单价。"
                : "请填写已核实的适用费率，未知留空。"
            }
          >
            <input
              type="number"
              min="0"
              step=".001"
              value={m[k] < 0 ? "" : m[k] / 1000}
              onChange={(e) =>
                set(
                  k,
                  e.target.value === ""
                    ? -1
                    : Math.round(Number(e.target.value) * 1000),
                )
              }
            />
          </Field>
        ))}
        <label className="check">
          <input
            type="checkbox"
            checked={!!m.temperature}
            onChange={(e) => set("temperature", e.target.checked)}
          />
          支持温度参数（0–2）
        </label>
      </div>
      <div className="actions">
        <Action
          className="primary"
          action={async () => {
            await api(
              "/api/models",
              modelPayload({ ...m, alias: m.alias || m.upstream }),
            );
            await refresh();
            close();
          }}
        >
          保存模型
        </Action>
      </div>
    </Modal>
  );
}
function Importer({ close, refresh, models }) {
  const [p, setP] = useState("deepseek"),
    [list, setList] = useState([]),
    [selected, setSelected] = useState([]),
    [kind, setKind] = useState("chat");
  return (
    <Modal title="从渠道导入模型" close={close}>
      <div className="row">
        <select
          value={p}
          onChange={(e) => {
            setP(e.target.value);
            setList([]);
            setSelected([]);
          }}
        >
          {Object.entries(providers).map(([k, v]) => (
            <option key={k} value={k}>
              {v}
            </option>
          ))}
        </select>
        <Action
          action={async () => {
            const r = await api("/api/discover?provider=" + p);
            setList(r);
            setSelected([]);
          }}
        >
          获取列表
        </Action>
      </div>
      <Field title="导入类型">
        <select value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="chat">聊天</option>
          <option value="embedding">文本向量</option>
        </select>
      </Field>
      <div className="choices">
        {list.map((id) => {
          const exists = models.some((m) => m.alias === id);
          return (
            <label key={id}>
              <input
                type="checkbox"
                disabled={exists}
                checked={exists || selected.includes(id)}
                onChange={(e) =>
                  setSelected(
                    e.target.checked
                      ? [...selected, id]
                      : selected.filter((x) => x !== id),
                  )
                }
              />
              {id}
              {exists ? " · 已配置" : ""}
            </label>
          );
        })}
      </div>
      <Action
        className="primary"
        disabled={!selected.length}
        action={async () => {
          for (const id of selected)
            await api(
              "/api/models",
              modelPayload({
                ...initialModel,
                alias: id,
                upstream: id,
                provider: p,
                kind,
                temperature: 0,
              }),
            );
          await refresh();
          close();
        }}
      >
        导入所选（{selected.length}）
      </Action>
    </Modal>
  );
}
export function Models({ config, refresh }) {
  const [edit, setEdit] = useState(null),
    [imp, setImp] = useState(false),
    [fav, setFav] = useState(false),
    [filter, setFilter] = useState("");
  const models = config.models.filter(
    (m) =>
      (!fav || m.favorite) &&
      [m.alias, m.upstream, providers[m.provider]]
        .join(" ")
        .toLowerCase()
        .includes(filter.toLowerCase()),
  );
  return (
    <>
      <div className="title">
        <div>
          <h1>渠道与模型</h1>
        </div>
        <div className="actions">
          <button onClick={() => setImp(true)}>从渠道导入</button>
          <button className="primary" onClick={() => setEdit(initialModel)}>
            ＋ 添加模型
          </button>
        </div>
      </div>
      <div className="panel">
        <div className="toolbar">
          <input
            placeholder="搜索模型或渠道"
            aria-label="搜索模型或渠道"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
          <label className="check">
            <input
              type="checkbox"
              checked={fav}
              onChange={(e) => setFav(e.target.checked)}
            />
            仅看收藏
          </label>
        </div>
        <div className="tablewrap">
          <table>
            <thead>
              <tr>
                <th>收藏</th>
                <th>网关模型</th>
                <th>上游渠道</th>
                <th>类型</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {models.map((m) => (
                <tr key={m.alias}>
                  <td>
                    <Action
                      className="star"
                      aria-label={m.favorite ? "取消收藏" : "收藏模型"}
                      action={async () => {
                        await api("/api/favorite", {
                          alias: m.alias,
                          favorite: !m.favorite,
                        });
                        await refresh();
                      }}
                    >
                      {m.favorite ? "★" : "☆"}
                    </Action>
                  </td>
                  <td>
                    <b>{m.alias}</b>
                    <small>{m.upstream}</small>
                  </td>
                  <td>{providers[m.provider]}</td>
                  <td>{m.kind === "chat" ? "聊天" : "文本向量"}</td>
                  <td>
                    <button onClick={() => setEdit(m)}>编辑</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!models.length && <Blank>没有匹配的模型</Blank>}
      </div>
      {edit && (
        <ModelEditor
          model={edit}
          refresh={refresh}
          close={() => setEdit(null)}
        />
      )}{" "}
      {imp && (
        <Importer
          models={config.models}
          refresh={refresh}
          close={() => setImp(false)}
        />
      )}
    </>
  );
}
function KeyEditor({ item, models, refresh, close, created }) {
  const [name, setName] = useState(item?.name || ""),
    [budget, setBudget] = useState(
      item && item.budget >= 0 ? String(item.budget / unit) : "",
    ),
    [rpm, setRpm] = useState(item?.rpm ?? 60),
    [parallel, setParallel] = useState(item?.parallel ?? 4),
    [allowed, setAllowed] = useState(item ? JSON.parse(item.allowed) : ["*"]),
    [enabled, setEnabled] = useState(item ? !!item.enabled : true);
  return (
    <Modal title={item ? "编辑网关密钥" : "创建网关密钥"} close={close}>
      <Field title="名称">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="例如：开发测试"
        />
      </Field>
      <div className="formgrid">
        <Field title="额度（元）" help="留空为无限额度，0 表示零额度。">
          <input
            type="number"
            min="0"
            step="any"
            value={budget}
            onChange={(e) => setBudget(e.target.value)}
          />
        </Field>
        <Field title="每分钟请求上限" help="0 表示无限制。">
          <input
            type="number"
            value={rpm}
            onChange={(e) => setRpm(e.target.value)}
          />
        </Field>
        <Field title="在途请求上限" help="0 表示无限制。">
          <input
            type="number"
            value={parallel}
            onChange={(e) => setParallel(e.target.value)}
          />
        </Field>
        <label className="check">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          启用密钥
        </label>
      </div>
      <h3>可用模型</h3>
      <div className="choices">
        {["*", ...models.map((m) => m.alias)].map((id) => (
          <label key={id}>
            <input
              type="checkbox"
              checked={allowed.includes(id)}
              disabled={id !== "*" && allowed.includes("*")}
              onChange={(e) =>
                setAllowed(
                  e.target.checked
                    ? id === "*"
                      ? ["*"]
                      : [...allowed, id]
                    : allowed.filter((i) => i !== id),
                )
              }
            />
            {id === "*" ? "全部模型（含以后导入）" : id}
          </label>
        ))}
      </div>
      <Action
        className="primary"
        action={async () => {
          const res = await api("/api/keys", {
            id: item?.id || "",
            name,
            enabled,
            allowed,
            rpm: Number(rpm),
            parallel: Number(parallel),
            budget: budget === "" ? -1 : Math.round(Number(budget) * unit),
          });
          await refresh();
          close();
          if (res.secret) created(res.secret);
        }}
      >
        保存密钥
      </Action>
    </Modal>
  );
}
export function Keys({ config, refresh }) {
  const [edit, setEdit] = useState(undefined),
    [secret, setSecret] = useState("");
  return (
    <>
      <div className="title">
        <div>
          <h1>网关密钥</h1>
        </div>
        <button className="primary" onClick={() => setEdit(null)}>
          ＋ 创建密钥
        </button>
      </div>
      <div className="panel tablewrap">
        <table>
          <thead>
            <tr>
              <th>名称 / ID</th>
              <th>额度</th>
              <th>结算 / 占用</th>
              <th>RPM / 并发</th>
              <th>状态</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {config.keys.map((k) => (
              <tr key={k.id}>
                <td>
                  <b>{k.name}</b>
                  <small>{k.id}</small>
                </td>
                <td>{k.budget < 0 ? "无限" : money(k.budget)}</td>
                <td>
                  {money(k.spent)} / {money(k.held)}
                </td>
                <td>
                  {k.rpm || "∞"} / {k.parallel || "∞"}
                </td>
                <td>{k.enabled ? "已启用" : "已停用"}</td>
                <td>
                  <button onClick={() => setEdit(k)}>编辑</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        {!config.keys.length && (
          <Blank>创建第一把网关密钥，即可调用已配置的模型。</Blank>
        )}
      </div>
      {edit !== undefined && (
        <KeyEditor
          item={edit}
          models={config.models}
          created={setSecret}
          close={() => setEdit(undefined)}
          refresh={refresh}
        />
      )}{" "}
      {secret && (
        <Modal title="请保存新密钥" close={() => setSecret("")}>
          <p>完整密钥仅在此显示一次。</p>
          <input readOnly value={secret} onFocus={(e) => e.target.select()} />
          <button onClick={() => setSecret("")}>已保存</button>
        </Modal>
      )}
    </>
  );
}
const day = (d) =>
  [
    d.getFullYear(),
    String(d.getMonth() + 1).padStart(2, "0"),
    String(d.getDate()).padStart(2, "0"),
  ].join("-");
function midnight(date, next = false) {
  const d = new Date(date + "T00:00:00");
  if (next) d.setDate(d.getDate() + 1);
  return d.getTime();
}
export function Reports({ mode }) {
  const today = day(new Date()),
    [from, setFrom] = useState(mode === "logs" ? "" : today),
    [to, setTo] = useState(mode === "logs" ? "" : today),
    [id, setId] = useState(
      new URLSearchParams(location.hash.split("?")[1]).get("id") || "",
    ),
    [page, setPage] = useState(1),
    [refresh, setRefresh] = useState(0),
    [data, setData] = useState(null),
    [err, setErr] = useState(""),
    [selected, setSelected] = useState(null),
    [note, setNote] = useState(""),
    [cost, setCost] = useState("");
  const params = new URLSearchParams({
    ...(from ? { from: midnight(from) } : {}),
    ...(to ? { to: midnight(to, true) } : {}),
    page,
    ...(id ? { id } : {}),
  }).toString();
  useEffect(() => {
    let active = true;
    setErr("");
    api("/api/calls?" + params)
      .then((v) => {
        if (active) setData(v);
      })
      .catch((e) => {
        if (active) {
          setErr(e.message);
          setData(null);
        }
      });
    return () => {
      active = false;
    };
  }, [params, refresh]);
  const usage = mode === "usage";
  return (
    <>
      <div className="title">
        <div>
          <h1>{usage ? "用量与费用" : "调用日志"}</h1>
        </div>
        <a className="button" href={"/api/export?" + params} download>
          ↓ 导出 CSV
        </a>
      </div>
      <div className="toolbar panel">
        <Field title="开始日期">
          <input
            type="date"
            value={from}
            onChange={(e) => {
              setFrom(e.target.value);
              setPage(1);
            }}
          />
        </Field>
        <Field title="结束日期">
          <input
            type="date"
            value={to}
            onChange={(e) => {
              setTo(e.target.value);
              setPage(1);
            }}
          />
        </Field>
        {!usage && (
          <Field title="精确查询请求 ID">
            <input
              value={id}
              placeholder="req_…"
              onChange={(e) => {
                setId(e.target.value);
                setPage(1);
              }}
            />
          </Field>
        )}
        <button onClick={() => setRefresh(refresh + 1)}>刷新</button>
      </div>
      <Notice error={err} />
      {data && (
        <>
          {usage && (
            <>
              <div className="metrics">
                <div className="metric">
                  <span>区间已结算费用</span>
                  <strong>{money(data.total.cost)}</strong>
                </div>
                <div className="metric">
                  <span>待核实请求</span>
                  <strong>{data.total.pending}</strong>
                </div>
                <div className="metric">
                  <span>占用额度</span>
                  <strong>{money(data.total.held)}</strong>
                </div>
                <div className="metric">
                  <span>请求数</span>
                  <strong>{data.total.count}</strong>
                </div>
              </div>
              <h2>按模型汇总</h2>
              <div className="panel tablewrap">
                <table>
                  <thead>
                    <tr>
                      <th>模型</th>
                      <th>请求数</th>
                      <th>已结算费用</th>
                      <th>待核实</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.models.map((m) => (
                      <tr key={m.model}>
                        <td>{m.model || "未路由"}</td>
                        <td>{m.count}</td>
                        <td>{money(m.cost)}</td>
                        <td>{m.pending}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
          <div className="sectionhead">
            <h2>调用明细</h2>
            <small>共 {data.total.count} 条</small>
          </div>
          <div className="panel tablewrap">
            <table>
              <thead>
                <tr>
                  <th>请求 / 时间</th>
                  <th>模型</th>
                  <th>输入 / 输出</th>
                  <th>费用</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {data.rows.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <button
                        className="link mono"
                        onClick={() => {
                          setSelected(r);
                          setNote("");
                          setCost("");
                        }}
                      >
                        {r.id}
                      </button>
                      <small>{when(r.created)}</small>
                    </td>
                    <td>{r.model || "—"}</td>
                    <td>
                      {r.input ?? "—"} / {r.output ?? "—"}
                    </td>
                    <td>
                      {money(r.cost)}
                      {r.cost == null && r.estimate != null && (
                        <small>估算 {money(r.estimate)}</small>
                      )}
                    </td>
                    <td>
                      <Status value={r.state} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {!data.rows.length && <Blank>所选范围内没有记录</Blank>}
          </div>
          <div className="actions pager">
            <button disabled={page === 1} onClick={() => setPage(page - 1)}>
              上一页
            </button>
            <small>第 {page} 页</small>
            <button
              disabled={page * 20 >= data.total.count}
              onClick={() => setPage(page + 1)}
            >
              下一页
            </button>
          </div>
        </>
      )}
      {selected && (
        <Modal title="调用详情" close={() => setSelected(null)}>
          <dl>
            {Object.entries({
              "请求 ID": selected.id,
              "厂商请求 ID": selected.up_id || "—",
              时间: when(selected.created),
              "Key ID": selected.key_id,
              模型: selected.model,
              耗时: selected.duration + " ms",
              费用估算:
                selected.estimate == null ? "不可用" : money(selected.estimate),
              预留: money(selected.hold),
              备注: selected.note || "—",
            }).map(([k, v]) => (
              <React.Fragment key={k}>
                <dt>{k}</dt>
                <dd>{v}</dd>
              </React.Fragment>
            ))}
          </dl>
          {selected.state === "pending" && (
            <>
              <h3>按厂商账单核实</h3>
              <Field title="实际费用（元）">
                <input
                  type="number"
                  min="0"
                  step="any"
                  value={cost}
                  onChange={(e) => setCost(e.target.value)}
                />
              </Field>
              <Field title="核实依据">
                <input
                  value={note}
                  onChange={(e) => setNote(e.target.value)}
                  placeholder="账单流水或未计费凭据"
                />
              </Field>
              <Action
                className="primary"
                disabled={cost === "" || !note.trim()}
                action={async () => {
                  await api("/api/reconcile", {
                    id: selected.id,
                    cost: Math.round(Number(cost) * unit),
                    note,
                  });
                  setSelected(null);
                  setRefresh(refresh + 1);
                }}
              >
                确认结算并释放预留
              </Action>
            </>
          )}
        </Modal>
      )}
    </>
  );
}
