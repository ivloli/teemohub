import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  api,
  fmtCNY,
  fmtNum,
  fmtTime,
  type APIKey,
  type LedgerEntry,
  type ModelPriceVersion,
  type PublicModel,
  type RequestRow,
  type UsageEvent,
} from "../api";
import { StatusBadge, TrustBadge } from "../components/Badge";
import { Icon } from "../components/Icon";
import { PageState } from "../components/PageState";
import { csvStamp, downloadCsv } from "../lib/csv";

const PAGE = 20;
const EXPORT_PAGE = 200;
const EXPORT_MAX = 10000;

const EXPORT_HEADERS = [
  "请求 ID",
  "trace ID",
  "模型",
  "密钥 ID",
  "状态",
  "HTTP",
  "流式",
  "输入 tokens",
  "缓存输入 tokens",
  "输出 tokens",
  "推理 tokens",
  "费用(CNY)",
  "供应商成本(CNY)",
  "预留(CNY)",
  "价格版本 ID",
  "延迟(ms)",
  "首 token(ms)",
  "创建时间",
  "结束时间",
  "客户端断开",
  "上游完成",
  "用量来源",
  "用量可信度",
  "错误",
];

function requestExportRow(r: RequestRow): unknown[] {
  return [
    r.id,
    r.trace_id,
    r.model,
    r.api_key_id,
    r.status,
    r.http_status,
    r.stream ? "是" : "否",
    r.input_tokens,
    r.cached_input_tokens,
    r.output_tokens,
    r.reasoning_tokens,
    (r.cost_nano / 1e8).toFixed(8),
    (r.provider_cost_nano / 1e8).toFixed(8),
    (r.reserved_nano / 1e8).toFixed(8),
    r.price_version_id || "",
    r.latency_ms,
    r.first_token_ms,
    fmtTime(r.created_at),
    r.finished_at ? fmtTime(r.finished_at) : "",
    r.client_disconnected ? "是" : "否",
    r.upstream_finished ? "是" : "否",
    r.usage_source,
    r.usage_trust,
    r.error_message,
  ];
}

export default function Logs() {
  const { id } = useParams();
  const nav = useNavigate();
  const [params, setParams] = useSearchParams();

  const model = params.get("model") || "";
  const status = params.get("status") || "";
  const q = params.get("q") || "";
  const keyID = params.get("key_id") || "";
  const offset = Math.max(0, Number(params.get("offset") || 0));

  const patch = useCallback(
    (next: Record<string, string | null>) => {
      const p = new URLSearchParams(params);
      for (const [k, v] of Object.entries(next)) {
        if (v) p.set(k, v);
        else p.delete(k);
      }
      if (!("offset" in next)) p.delete("offset");
      setParams(p, { replace: true });
    },
    [params, setParams],
  );

  const [items, setItems] = useState<RequestRow[]>([]);
  const [total, setTotal] = useState(0);
  const [models, setModels] = useState<PublicModel[]>([]);
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [draft, setDraft] = useState(q);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [exporting, setExporting] = useState(false);

  useEffect(() => {
    setDraft(q);
  }, [q]);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const qs = new URLSearchParams({ limit: String(PAGE), offset: String(offset) });
      if (model) qs.set("model", model);
      if (status) qs.set("status", status);
      if (q) qs.set("q", q);
      if (keyID) qs.set("key_id", keyID);
      const r = await api<{ items: RequestRow[]; total: number }>(`/api/requests?${qs}`);
      setItems(r.items || []);
      setTotal(r.total || 0);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [offset, model, status, q, keyID]);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    api<{ items: PublicModel[] }>("/api/models")
      .then((r) => setModels(r.items || []))
      .catch(() => undefined);
    api<{ items: APIKey[] }>("/api/keys")
      .then((r) => setKeys((r.items || []).filter((k) => k.status === 1)))
      .catch(() => undefined);
  }, []);

  const hasFilters = Boolean(model || status || q || keyID);
  const backSuffix = params.toString() ? `?${params}` : "";

  async function exportAll() {
    setExporting(true);
    setError("");
    try {
      const rows: RequestRow[] = [];
      for (let off = 0; off < EXPORT_MAX; off += EXPORT_PAGE) {
        const qs = new URLSearchParams({ limit: String(EXPORT_PAGE), offset: String(off) });
        if (model) qs.set("model", model);
        if (status) qs.set("status", status);
        if (q) qs.set("q", q);
        if (keyID) qs.set("key_id", keyID);
        const r = await api<{ items: RequestRow[] }>(`/api/requests?${qs}`);
        const page = r.items || [];
        rows.push(...page);
        if (page.length < EXPORT_PAGE) break;
      }
      downloadCsv(`teemohub-requests-${csvStamp()}.csv`, EXPORT_HEADERS, rows.map(requestExportRow));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setExporting(false);
    }
  }

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>请求日志</h1>
          <p className="muted">请求级用量、来源阶梯、账本流水与延迟，全部来自本机 SQLite。</p>
        </div>
        <div className="head-actions">
          <button className="btn ghost" onClick={exportAll} disabled={exporting}>
            <Icon name="download" size={16} /> {exporting ? "导出中…" : "导出 CSV"}
          </button>
          <button className="btn ghost" onClick={load}>
            <Icon name="refresh" size={16} /> 刷新
          </button>
        </div>
      </header>

      {error && <div className="alert error">{error}</div>}

      <section className="card">
        <div className="filter-bar">
          <label className="field grow search-field">
            <Icon name="search" size={15} />
            <input
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") patch({ q: draft.trim() || null });
              }}
              placeholder="搜索 request id 或 trace id，回车确认"
            />
          </label>
          <label className="field">
            <span>模型</span>
            <select value={model} onChange={(e) => patch({ model: e.target.value || null })}>
              <option value="">全部</option>
              {models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.display_name}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>状态</span>
            <select value={status} onChange={(e) => patch({ status: e.target.value || null })}>
              <option value="">全部</option>
              <option value="completed">completed</option>
              <option value="drained">drained</option>
              <option value="client_aborted">client_aborted</option>
              <option value="upstream_error">upstream_error</option>
            </select>
          </label>
          <label className="field">
            <span>密钥</span>
            <select value={keyID} onChange={(e) => patch({ key_id: e.target.value || null })}>
              <option value="">全部</option>
              {keys.map((k) => (
                <option key={k.id} value={String(k.id)}>
                  {k.name} · {k.prefix}…{k.suffix}
                </option>
              ))}
            </select>
          </label>
          {hasFilters && (
            <button className="btn ghost" onClick={() => patch({ model: null, status: null, q: null, key_id: null })}>
              清除筛选
            </button>
          )}
          <span className="muted small filter-total">共 {total} 条</span>
        </div>

        <PageState loading={loading} empty={items.length === 0} emptyText="没有匹配的请求。">
          <div className="table-wrap">
            <table className="clickable">
              <thead>
                <tr>
                  <th>时间</th>
                  <th>请求 / 模型</th>
                  <th>状态</th>
                  <th>用量来源</th>
                  <th className="num">入 / 出</th>
                  <th className="num">费用</th>
                  <th className="num">供应商成本</th>
                  <th className="num">延迟</th>
                </tr>
              </thead>
              <tbody>
                {items.map((r) => (
                  <tr key={r.id} onClick={() => nav(`/logs/${r.id}${backSuffix}`)}>
                    <td className="muted small nowrap">{fmtTime(r.created_at)}</td>
                    <td>
                      <div className="mono small">{r.id}</div>
                      <div className="muted small">
                        {r.model}
                        {r.stream ? " · stream" : ""}
                      </div>
                    </td>
                    <td>
                      <StatusBadge status={r.status} />
                    </td>
                    <td>
                      <TrustBadge source={r.usage_source} />
                    </td>
                    <td className="num">
                      {fmtNum(r.input_tokens)} / {fmtNum(r.output_tokens)}
                      {r.cached_input_tokens > 0 && <div className="muted small">缓存 {fmtNum(r.cached_input_tokens)}</div>}
                    </td>
                    <td className="num strong">{fmtCNY(r.cost_nano)}</td>
                    <td className="num muted">{fmtCNY(r.provider_cost_nano)}</td>
                    <td className="num muted">{r.latency_ms}ms</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </PageState>

        <div className="pager">
          <button className="btn ghost" disabled={offset === 0} onClick={() => patch({ offset: String(Math.max(0, offset - PAGE)) })}>
            上一页
          </button>
          <span className="muted small">
            {total === 0 ? 0 : offset + 1}–{Math.min(offset + PAGE, total)} / {total}
          </span>
          <button className="btn ghost" disabled={offset + PAGE >= total} onClick={() => patch({ offset: String(offset + PAGE) })}>
            下一页
          </button>
        </div>
      </section>

      {id && <Detail id={id} onClose={() => nav(`/logs${backSuffix}`)} />}
    </div>
  );
}

function Detail({ id, onClose }: { id: string; onClose: () => void }) {
  type DetailData = {
    request: RequestRow;
    usage_events: UsageEvent[];
    ledger: LedgerEntry[];
    price_version: ModelPriceVersion | null;
  };
  const [data, setData] = useState<DetailData | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    setData(null);
    setError("");
    api<DetailData>(`/api/requests/${id}`)
      .then(setData)
      .catch((e) => setError((e as Error).message));
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal wide" onClick={(e) => e.stopPropagation()}>
        <div className="modal-head">
          <div>
            <h2>请求详情</h2>
            <code className="muted small">{id}</code>
          </div>
          <button className="icon-btn" onClick={onClose}>
            ✕
          </button>
        </div>
        {error ? (
          <PageState error={error} onRetry={load} />
        ) : !data ? (
          <PageState loading loadingText="加载详情…" />
        ) : (
          <div className="detail-body">
            <div className="detail-summary">
              <div>
                <span className="muted small">状态</span>
                <StatusBadge status={data.request.status} />
              </div>
              <div>
                <span className="muted small">费用</span>
                <strong>{fmtCNY(data.request.cost_nano)}</strong>
              </div>
              <div>
                <span className="muted small">用量来源</span>
                <TrustBadge source={data.request.usage_source} />
              </div>
              <div>
                <span className="muted small">供应商成本</span>
                <strong>{fmtCNY(data.request.provider_cost_nano)}</strong>
              </div>
            </div>

            <Timeline r={data.request} />

            <dl className="kv wide">
              <div>
                <dt>模型</dt>
                <dd className="mono">{data.request.model}</dd>
              </div>
              <div>
                <dt>流式</dt>
                <dd>{data.request.stream ? "是" : "否"}</dd>
              </div>
              <div>
                <dt>输入 / 输出 / 推理</dt>
                <dd>
                  {fmtNum(data.request.input_tokens)} / {fmtNum(data.request.output_tokens)} / {fmtNum(data.request.reasoning_tokens)}
                </dd>
              </div>
              <div>
                <dt>缓存命中 tokens</dt>
                <dd>{fmtNum(data.request.cached_input_tokens)}</dd>
              </div>
              <div>
                <dt>延迟 / 首 token</dt>
                <dd>
                  {data.request.latency_ms}ms / {data.request.first_token_ms ? `${data.request.first_token_ms}ms` : "-"}
                </dd>
              </div>
              <div>
                <dt>预留 / 价格版本</dt>
                <dd>
                  {fmtCNY(data.request.reserved_nano)} /{" "}
                  {data.price_version ? `v${data.price_version.id}` : data.request.price_version_id ? `#${data.request.price_version_id}` : "未版本化"}
                </dd>
              </div>
              {data.price_version && (
                <div>
                  <dt>当时单价（CNY/M）</dt>
                  <dd className="small">
                    入 {(data.price_version.price.input_price_nano / 100).toFixed(2)} · 缓存入{" "}
                    {(data.price_version.price.cached_input_price_nano / 100).toFixed(2)} · 出{" "}
                    {(data.price_version.price.output_price_nano / 100).toFixed(2)}
                    {data.price_version.note ? <div className="muted">备注：{data.price_version.note}</div> : null}
                  </dd>
                </div>
              )}
              <div>
                <dt>客户端断开 / 上游完成</dt>
                <dd>
                  {data.request.client_disconnected ? "是" : "否"} / {data.request.upstream_finished ? "是" : "否"}
                </dd>
              </div>
              <div>
                <dt>HTTP</dt>
                <dd>{data.request.http_status}</dd>
              </div>
              <div>
                <dt>trace</dt>
                <dd className="mono small">{data.request.trace_id}</dd>
              </div>
            </dl>

            {data.request.error_message && <div className="alert error">{data.request.error_message}</div>}

            <h3>用量事件</h3>
            {data.usage_events.length === 0 ? (
              <div className="empty">没有用量事件。</div>
            ) : (
              data.usage_events.map((ev) => (
                <div className="usage-event" key={ev.id}>
                  <div className="usage-event-head">
                    <span>
                      attempt {ev.attempt} · final={String(ev.final)}
                    </span>
                    <span>
                      <TrustBadge source={ev.source} /> trust {ev.trust}
                    </span>
                  </div>
                  <dl className="kv wide">
                    <div>
                      <dt>快照序号</dt>
                      <dd>{ev.snapshot_seq}</dd>
                    </div>
                    <div>
                      <dt>快照时本地输出</dt>
                      <dd>{fmtNum(ev.local_tokens_at_snapshot)}</dd>
                    </div>
                    <div>
                      <dt>最终本地输出</dt>
                      <dd>{fmtNum(ev.local_output_tokens)}</dd>
                    </div>
                    <div>
                      <dt>推理 tokens</dt>
                      <dd>{fmtNum(ev.reasoning_tokens)}</dd>
                    </div>
                  </dl>
                  <pre className="json">{pretty(ev.detail_json)}</pre>
                </div>
              ))
            )}

            <h3>账本流水</h3>
            {data.ledger.length === 0 ? (
              <div className="empty">没有账本流水。</div>
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>类型</th>
                      <th className="num">金额</th>
                      <th className="num">余额</th>
                      <th>备注</th>
                    </tr>
                  </thead>
                  <tbody>
                    {data.ledger.map((l) => (
                      <tr key={l.id}>
                        <td className="muted small">{fmtTime(l.created_at)}</td>
                        <td>
                          <span className={`badge ${l.entry_type === "reserve" ? "muted" : l.entry_type === "settle" ? "ok" : "warn"}`}>
                            {l.entry_type}
                          </span>
                        </td>
                        <td className={`num ${l.amount_nano < 0 ? "neg" : "pos"}`}>{fmtCNY(l.amount_nano)}</td>
                        <td className="num">{fmtCNY(l.balance_after_nano)}</td>
                        <td className="muted small">{l.memo}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

function Timeline({ r }: { r: RequestRow }) {
  const bad = r.status === "upstream_error";
  const steps = [{ label: "收到请求", detail: `T+0ms · ${fmtTime(r.created_at)}` }];
  if (r.first_token_ms > 0) steps.push({ label: "首个 token", detail: `T+${r.first_token_ms}ms` });
  steps.push({
    label: r.finished_at ? "请求结束" : "未收到结束",
    detail: `T+${r.latency_ms}ms · ${r.status}${r.http_status ? ` · HTTP ${r.http_status}` : ""}`,
  });
  return (
    <div className="timeline">
      {steps.map((s, i) => {
        const last = i === steps.length - 1;
        return (
          <div className={`timeline-row${last ? " last" : ""}${last && bad ? " is-bad" : ""}`} key={s.label}>
            <span className="timeline-dot" />
            <div className="timeline-body">
              <strong>{s.label}</strong>
              <span className="muted small">{s.detail}</span>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function pretty(s: string): string {
  try {
    return JSON.stringify(JSON.parse(s), null, 2);
  } catch {
    return s;
  }
}
