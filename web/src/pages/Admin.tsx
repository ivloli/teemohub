import { Fragment, useCallback, useEffect, useState } from "react";
import {
  api,
  fmtCNY,
  fmtTime,
  type AuditEvent,
  type Model,
  type ModelPriceVersion,
  type User,
} from "../api";
import { Icon } from "../components/Icon";
import { PageState } from "../components/PageState";
import { Spinner } from "../components/Spinner";

type Tab = "upstream" | "users" | "models" | "audit";

export default function Admin() {
  const [tab, setTab] = useState<Tab>("upstream");
  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>管理</h1>
          <p className="muted">上游健康、用户与模型定价。New API 仅作为上游，不在此修改其数据。</p>
        </div>
      </header>
      <div className="tabs">
        <button className={tab === "upstream" ? "active" : ""} onClick={() => setTab("upstream")}>
          上游
        </button>
        <button className={tab === "users" ? "active" : ""} onClick={() => setTab("users")}>
          用户
        </button>
        <button className={tab === "models" ? "active" : ""} onClick={() => setTab("models")}>
          模型与定价
        </button>
        <button className={tab === "audit" ? "active" : ""} onClick={() => setTab("audit")}>
          审计日志
        </button>
      </div>
      {tab === "upstream" && <Upstream />}
      {tab === "users" && <Users />}
      {tab === "models" && <Models />}
      {tab === "audit" && <Audit />}
    </div>
  );
}

function Upstream() {
  const [data, setData] = useState<{ new_api_base_url: string; healthy: boolean; error: string; models: Model[] } | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setData(await api("/api/admin/upstream"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <section className="card">
      <div className="card-head">
        <h2>New API 上游</h2>
        <button className="btn ghost" onClick={load}>
          <Icon name="refresh" size={16} /> 检查
        </button>
      </div>
      {loading ? (
        <Spinner />
      ) : (
        <>
          <div className={`upstream-card ${data?.healthy ? "ok" : "bad"}`}>
            <span className={`upstream-dot ${data?.healthy ? "on" : "off"}`} />
            <div>
              <strong>{data?.healthy ? "已连接" : "不可用"}</strong>
              <div className="muted small mono">{data?.new_api_base_url}</div>
            </div>
            {data?.error && <span className="muted small">{data.error}</span>}
          </div>
          <p className="muted small">
            网关把请求转发给 New API 的 <code>/v1/chat/completions</code>，由 New API 选择渠道并访问 DeepSeek / Kimi。
            teemoHub 只保存 New API 的网关 token，不保存供应商密钥。
          </p>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>teemoHub 模型</th>
                  <th>上游模型名</th>
                  <th>家族</th>
                  <th className="num">输入价 (CNY/M)</th>
                  <th className="num">缓存入价 (CNY/M)</th>
                  <th className="num">输出价 (CNY/M)</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                {(data?.models || []).map((m) => (
                  <tr key={m.id}>
                    <td className="mono">{m.id}</td>
                    <td className="mono muted">{m.upstream_name}</td>
                    <td>{m.family}</td>
                    <td className="num">{(m.input_price_nano / 100).toFixed(2)}</td>
                    <td className="num">{m.cached_input_price_nano > 0 ? (m.cached_input_price_nano / 100).toFixed(2) : "—"}</td>
                    <td className="num">{(m.output_price_nano / 100).toFixed(2)}</td>
                    <td>
                      <span className={`badge ${m.enabled ? "ok" : "muted"}`}>{m.enabled ? "启用" : "停用"}</span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}
    </section>
  );
}

function Users() {
  const [users, setUsers] = useState<User[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [form, setForm] = useState({ username: "", password: "", display_name: "", role: "user", balance_cny: 100 });
  const [busy, setBusy] = useState(false);
  const [topup, setTopup] = useState<User | null>(null);
  const [amount, setAmount] = useState(100);
  const [memo, setMemo] = useState("manual topup");

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await api<{ items: User[] }>("/api/admin/users");
      setUsers(r.items || []);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api("/api/admin/users", { method: "POST", body: JSON.stringify(form) });
      setForm({ username: "", password: "", display_name: "", role: "user", balance_cny: 100 });
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function doTopup() {
    if (!topup) return;
    setBusy(true);
    try {
      await api(`/api/admin/users/${topup.id}/topup`, {
        method: "POST",
        body: JSON.stringify({ amount_cny: amount, memo }),
      });
      setTopup(null);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      {error && <div className="alert error">{error}</div>}
      <div className="two-col admin-users">
        <section className="card">
          <div className="card-head">
            <h2>新建用户</h2>
          </div>
          <form className="form" onSubmit={create}>
            <label>
              用户名
              <input value={form.username} onChange={(e) => setForm({ ...form, username: e.target.value })} />
            </label>
            <label>
              显示名
              <input value={form.display_name} onChange={(e) => setForm({ ...form, display_name: e.target.value })} />
            </label>
            <label>
              初始密码（至少 8 位）
              <input type="password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
            </label>
            <label>
              角色
              <select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
                <option value="user">user</option>
                <option value="admin">admin</option>
              </select>
            </label>
            <label>
              初始余额（CNY）
              <input type="number" step="0.01" min={0} value={form.balance_cny} onChange={(e) => setForm({ ...form, balance_cny: Number(e.target.value) })} />
            </label>
            <button className="btn primary" disabled={busy || !form.username || form.password.length < 8}>
              <Icon name="plus" size={16} /> 创建用户
            </button>
          </form>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>用户</h2>
            <span className="muted small">{users.length} 人</span>
          </div>
          <PageState loading={loading} empty={users.length === 0} emptyText="还没有用户。">
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>用户</th>
                    <th>角色</th>
                    <th className="num">余额</th>
                    <th>创建时间</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {users.map((u) => (
                    <tr key={u.id}>
                      <td>
                        <div>{u.display_name}</div>
                        <div className="muted small mono">{u.username}</div>
                      </td>
                      <td>
                        <span className={`badge ${u.role === "admin" ? "violet" : "muted"}`}>{u.role}</span>
                      </td>
                      <td className="num strong">{fmtCNY(u.balance_nano)}</td>
                      <td className="muted small">{fmtTime(u.created_at)}</td>
                      <td>
                        <button className="btn ghost small-btn" onClick={() => { setTopup(u); setAmount(100); }}>
                          充值
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </PageState>
        </section>
      </div>

      {topup && (
        <div className="modal-backdrop" onClick={() => setTopup(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>为 {topup.display_name} 充值</h2>
            <p className="muted">写入一条 topup 账本流水，余额立即生效。</p>
            <label className="field">
              <span>金额（CNY）</span>
              <input type="number" step="0.01" value={amount} onChange={(e) => setAmount(Number(e.target.value))} />
            </label>
            <label className="field">
              <span>备注</span>
              <input value={memo} onChange={(e) => setMemo(e.target.value)} />
            </label>
            <div className="modal-actions">
              <button className="btn" onClick={() => setTopup(null)}>
                取消
              </button>
              <button className="btn primary" onClick={doTopup} disabled={busy || amount === 0}>
                确认充值
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}

function Models() {
  const [models, setModels] = useState<Model[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [drafts, setDrafts] = useState<Record<string, Record<string, string | boolean>>>({});
  const [saving, setSaving] = useState("");
  const [open, setOpen] = useState("");
  const [versions, setVersions] = useState<Record<string, ModelPriceVersion[]>>({});

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await api<{ items: Model[] }>("/api/admin/models");
      setModels(r.items || []);
      const d: Record<string, Record<string, string | boolean>> = {};
      for (const m of r.items || []) {
        d[m.id] = {
          input: (m.input_price_nano / 100).toFixed(4),
          output: (m.output_price_nano / 100).toFixed(4),
          cin: (m.cached_input_price_nano / 100).toFixed(4),
          pin: (m.provider_input_price_nano / 100).toFixed(4),
          pout: (m.provider_output_price_nano / 100).toFixed(4),
          pcin: (m.provider_cached_input_price_nano / 100).toFixed(4),
          enabled: m.enabled,
          note: "",
        };
      }
      setDrafts(d);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  function setDraft(id: string, k: string, v: string | boolean) {
    setDrafts((prev) => ({ ...prev, [id]: { ...prev[id], [k]: v } }));
  }

  const loadVersions = useCallback(async (id: string) => {
    const r = await api<{ items: ModelPriceVersion[] }>(`/api/admin/models/${encodeURIComponent(id)}/versions?limit=20`);
    setVersions((prev) => ({ ...prev, [id]: r.items || [] }));
  }, []);

  async function toggle(m: Model) {
    if (open === m.id) {
      setOpen("");
      return;
    }
    setOpen(m.id);
    setError("");
    try {
      await loadVersions(m.id);
    } catch (e) {
      setError((e as Error).message);
    }
  }

  async function save(m: Model) {
    const d = drafts[m.id];
    setSaving(m.id);
    setError("");
    try {
      await api(`/api/admin/models/${m.id}`, {
        method: "PUT",
        body: JSON.stringify({
          input_price_cny_per_m: Number(d.input),
          output_price_cny_per_m: Number(d.output),
          cached_input_price_cny_per_m: Number(d.cin),
          provider_input_price_cny_per_m: Number(d.pin),
          provider_output_price_cny_per_m: Number(d.pout),
          provider_cached_input_price_cny_per_m: Number(d.pcin),
          enabled: Boolean(d.enabled),
          note: String(d.note || ""),
        }),
      });
      await load();
      if (open === m.id) await loadVersions(m.id);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving("");
    }
  }

  return (
    <section className="card">
      <div className="card-head">
        <h2>模型与定价</h2>
        <span className="muted small">单位：CNY / 百万 tokens；改价会在下次请求生效，并生成新的价格版本</span>
      </div>
      {error && <div className="alert error">{error}</div>}
      {loading ? (
        <Spinner />
      ) : (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th>模型</th>
                <th>上游名</th>
                <th className="num">客户 入</th>
                <th className="num">客户 缓存入</th>
                <th className="num">客户 出</th>
                <th className="num">供应商 入</th>
                <th className="num">供应商 缓存入</th>
                <th className="num">供应商 出</th>
                <th>启用</th>
                <th>备注（可选）</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {models.map((m) => {
                const d = drafts[m.id] || {};
                const isOpen = open === m.id;
                const list = versions[m.id];
                return (
                  <Fragment key={m.id}>
                    <tr>
                      <td>
                        <div className="mono">{m.id}</div>
                        <div className="muted small">{m.display_name}</div>
                      </td>
                      <td className="mono muted small">{m.upstream_name}</td>
                      {(["input", "cin", "output", "pin", "pcin", "pout"] as const).map((k) => (
                        <td className="num" key={k}>
                          <input
                            className="num-input"
                            value={String(d[k] ?? "")}
                            onChange={(e) => setDraft(m.id, k, e.target.value)}
                          />
                        </td>
                      ))}
                      <td>
                        <input
                          type="checkbox"
                          checked={Boolean(d.enabled)}
                          onChange={(e) => setDraft(m.id, "enabled", e.target.checked)}
                        />
                      </td>
                      <td>
                        <input
                          className="note-input"
                          placeholder="例如：跟随 DeepSeek 降价"
                          value={String(d.note ?? "")}
                          onChange={(e) => setDraft(m.id, "note", e.target.value)}
                        />
                      </td>
                      <td>
                        <div className="row-actions">
                          <button className="btn ghost small-btn" onClick={() => save(m)} disabled={saving === m.id}>
                            {saving === m.id ? "保存中…" : "保存"}
                          </button>
                          <button className="btn ghost small-btn" onClick={() => toggle(m)}>
                            {isOpen ? "收起" : `版本 v${m.price_version_id || "—"}`}
                          </button>
                        </div>
                      </td>
                    </tr>
                    {isOpen && (
                      <tr>
                        <td colSpan={11}>
                          <div className="version-panel">
                            <div className="muted small">
                              价格版本为只追加记录：每次改价写入一行并冻结，历史请求保留当时的版本号。
                            </div>
                            {!list ? (
                              <Spinner />
                            ) : list.length === 0 ? (
                              <p className="muted small">暂无版本记录。</p>
                            ) : (
                              <ol className="version-timeline">
                                {list.map((v) => (
                                  <li key={v.id}>
                                    <div className="version-head">
                                      <span className="mono strong">v{v.id}</span>
                                      <span className="muted small">{fmtTime(v.created_at)}</span>
                                      <span className="muted small mono">{v.actor}</span>
                                      {v.id === m.price_version_id && <span className="badge ok">当前</span>}
                                    </div>
                                    <div className="version-prices small">
                                      <span>客户 入 {(v.price.input_price_nano / 100).toFixed(2)}</span>
                                      <span>缓存入 {(v.price.cached_input_price_nano / 100).toFixed(2)}</span>
                                      <span>出 {(v.price.output_price_nano / 100).toFixed(2)}</span>
                                      <span className="muted">供应商 入 {(v.price.provider_input_price_nano / 100).toFixed(2)}</span>
                                      <span className="muted">缓存入 {(v.price.provider_cached_input_price_nano / 100).toFixed(2)}</span>
                                      <span className="muted">出 {(v.price.provider_output_price_nano / 100).toFixed(2)}</span>
                                    </div>
                                    {v.note && <div className="muted small">备注：{v.note}</div>}
                                  </li>
                                ))}
                              </ol>
                            )}
                          </div>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

const AUDIT_RESOURCES = ["", "model", "user", "key", "request", "settings"] as const;

function pretty(raw: string): string {
  if (!raw) return "";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}

function Audit() {
  const [items, setItems] = useState<AuditEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [more, setMore] = useState(false);
  const [error, setError] = useState("");
  const [expanded, setExpanded] = useState<number | null>(null);
  const [filters, setFilters] = useState({ resource: "", action: "", q: "" });
  const [applied, setApplied] = useState({ resource: "", action: "", q: "" });
  const [done, setDone] = useState(false);

  const fetchPage = useCallback(async (beforeID: number, f: { resource: string; action: string; q: string }) => {
    const qs = new URLSearchParams();
    qs.set("limit", "50");
    if (f.resource) qs.set("resource", f.resource);
    if (f.action) qs.set("action", f.action);
    if (f.q) qs.set("q", f.q);
    if (beforeID > 0) qs.set("before_id", String(beforeID));
    return api<{ items: AuditEvent[] }>(`/api/admin/audit?${qs.toString()}`);
  }, []);

  const load = useCallback(async (f: { resource: string; action: string; q: string }) => {
    setLoading(true);
    setError("");
    setDone(false);
    try {
      const r = await fetchPage(0, f);
      setItems(r.items || []);
      setDone((r.items || []).length < 50);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [fetchPage]);

  useEffect(() => {
    load(applied);
  }, [applied, load]);

  async function loadMore() {
    const last = items[items.length - 1];
    if (!last) return;
    setMore(true);
    setError("");
    try {
      const r = await fetchPage(last.id, applied);
      const next = r.items || [];
      setItems((prev) => [...prev, ...next]);
      setDone(next.length < 50);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setMore(false);
    }
  }

  function apply(e: React.FormEvent) {
    e.preventDefault();
    setExpanded(null);
    setApplied({ ...filters });
  }

  function reset() {
    setFilters({ resource: "", action: "", q: "" });
    setApplied({ resource: "", action: "", q: "" });
    setExpanded(null);
  }

  return (
    <section className="card">
      <div className="card-head">
        <h2>审计日志</h2>
        <span className="muted small">管理端的写操作（改价、建用户、充值、发/吊销密钥）与业务写入同事务记录，只追加、不可改删。</span>
      </div>
      <form className="filter-bar" onSubmit={apply}>
        <label className="field">
          <span>资源</span>
          <select value={filters.resource} onChange={(e) => setFilters({ ...filters, resource: e.target.value })}>
            {AUDIT_RESOURCES.map((r) => (
              <option key={r} value={r}>
                {r || "全部"}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>动作</span>
          <input
            placeholder="如 model.prices.update"
            value={filters.action}
            onChange={(e) => setFilters({ ...filters, action: e.target.value })}
          />
        </label>
        <label className="field">
          <span>搜索</span>
          <input
            placeholder="资源 ID / 操作人 / 备注"
            value={filters.q}
            onChange={(e) => setFilters({ ...filters, q: e.target.value })}
          />
        </label>
        <button className="btn primary" type="submit">
          查询
        </button>
        <button className="btn ghost" type="button" onClick={reset}>
          重置
        </button>
      </form>
      {error && <div className="alert error">{error}</div>}
      <PageState loading={loading} empty={items.length === 0} emptyText="没有匹配的审计事件。">
        <>
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>时间</th>
                  <th>操作人</th>
                  <th>动作</th>
                  <th>资源</th>
                  <th>备注</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {items.map((ev) => {
                  const isOpen = expanded === ev.id;
                  return (
                    <Fragment key={ev.id}>
                      <tr>
                        <td className="muted small nowrap">{fmtTime(ev.created_at)}</td>
                        <td>
                          <div>{ev.actor_name || "—"}</div>
                          <div className="muted small">{ev.actor_type}</div>
                        </td>
                        <td className="mono small">{ev.action}</td>
                        <td className="mono small">
                          {ev.resource}
                          {ev.resource_id && <span className="muted"> / {ev.resource_id}</span>}
                        </td>
                        <td className="small">{ev.reason || <span className="muted">—</span>}</td>
                        <td>
                          <button className="btn ghost small-btn" onClick={() => setExpanded(isOpen ? null : ev.id)}>
                            {isOpen ? "收起" : "详情"}
                          </button>
                        </td>
                      </tr>
                      {isOpen && (
                        <tr>
                          <td colSpan={6}>
                            <div className="audit-diff">
                              <div>
                                <div className="muted small">变更前</div>
                                <pre className="code">{pretty(ev.before_json) || "—"}</pre>
                              </div>
                              <div>
                                <div className="muted small">变更后</div>
                                <pre className="code">{pretty(ev.after_json) || "—"}</pre>
                              </div>
                            </div>
                          </td>
                        </tr>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </table>
          </div>
          {!done && (
            <div className="row-actions center">
              <button className="btn ghost" onClick={loadMore} disabled={more}>
                {more ? "加载中…" : "加载更多"}
              </button>
            </div>
          )}
        </>
      </PageState>
    </section>
  );
}
