import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, fmtCNY, fmtCNYShort, fmtNum, fmtPct, type Analytics } from "../api";
import { StatusBadge } from "../components/Badge";
import { Icon } from "../components/Icon";
import { Spinner } from "../components/Spinner";

const RANGES = [7, 14, 30];

type Payload = { analytics: Analytics; provider_cost_hidden: boolean };

export default function Usage() {
  const [days, setDays] = useState(7);
  const [data, setData] = useState<Payload | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const r = await api<Payload>(`/api/usage/analytics?days=${days}`);
      setData(r);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [days]);

  useEffect(() => {
    load();
  }, [load]);

  const a = data?.analytics;
  const hidden = data?.provider_cost_hidden ?? true;
  const totals = a?.totals;
  const daily = a?.daily || [];
  const maxCost = Math.max(1, ...daily.map((d) => d.cost_nano));
  const totalCost = totals?.cost_nano || 0;
  const margin = totalCost - (totals?.provider_cost_nano || 0);

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>用量分析</h1>
          <p className="muted">
            区间消费、缓存命中与模型 / 状态 / 密钥分布，数据来自本机账本
            {hidden ? "（供应商成本仅管理员可见）" : "，含供应商成本与毛利"}。
          </p>
        </div>
        <div className="head-actions">
          <div className="range-tabs" role="tablist">
            {RANGES.map((d) => (
              <button key={d} className={d === days ? "on" : ""} onClick={() => setDays(d)}>
                近 {d} 天
              </button>
            ))}
          </div>
          <button className="btn ghost" onClick={load}>
            <Icon name="refresh" size={16} /> 刷新
          </button>
        </div>
      </header>

      {error && <div className="alert error">{error}</div>}

      {loading && !a ? (
        <Spinner label="汇总中…" />
      ) : !a || !totals ? null : (
        <>
          <section className="stat-grid">
            <Stat
              label="区间消费"
              value={fmtCNY(totalCost)}
              hint={hidden ? `近 ${days} 天 · 按结算金额` : `供应商成本 ${fmtCNYShort(totals.provider_cost_nano)}`}
              accent
            />
            <Stat
              label="请求数"
              value={fmtNum(totals.requests)}
              hint={`失败 ${totals.failed} · 断流 ${totals.disconnected}`}
            />
            <Stat
              label="缓存命中率"
              value={fmtPct(totals.cached_input_tokens, totals.input_tokens)}
              hint={`命中 ${fmtNum(totals.cached_input_tokens)} / 输入 ${fmtNum(totals.input_tokens)}`}
            />
            <Stat
              label="平均延迟"
              value={totals.avg_latency_ms ? `${totals.avg_latency_ms}ms` : "—"}
              hint={totals.avg_first_token_ms ? `首 token ${totals.avg_first_token_ms}ms` : "暂无首 token 样本"}
            />
          </section>

          {!hidden && (
            <section className="card margin-card">
              <div className="margin-line">
                <span className="muted small">毛利口径</span>
                <strong className={margin >= 0 ? "pos" : "neg"}>{fmtCNY(margin)}</strong>
                <span className="muted small">
                  客户消费 {fmtCNY(totalCost)} − 供应商成本 {fmtCNY(totals.provider_cost_nano)}
                </span>
              </div>
            </section>
          )}

          <section className="card chart-card">
            <div className="card-head">
              <h2>近 {days} 日消费</h2>
              <span className="muted small">
                输入 {fmtNum(totals.input_tokens)} · 输出 {fmtNum(totals.output_tokens)} tokens
              </span>
            </div>
            <div className={`bars ${days > 14 ? "dense" : ""}`}>
              {daily.map((d, i) => {
                const peak = d.cost_nano === maxCost && d.cost_nano > 0;
                const showLabel = days <= 14 || peak || i === daily.length - 1;
                return (
                  <div className="bar-col" key={d.date} title={`${d.date} · ${fmtCNY(d.cost_nano)} · ${d.requests} 次 · 失败 ${d.failed}`}>
                    <div className="bar-value">{d.cost_nano && showLabel ? fmtCNYShort(d.cost_nano) : ""}</div>
                    <div className="bar-track">
                      <div
                        className={`bar-fill ${peak ? "peak" : ""}`}
                        style={{ height: `${Math.max(d.cost_nano ? 4 : 0, (d.cost_nano / maxCost) * 100)}%` }}
                      />
                    </div>
                    <div className="bar-label">{days <= 14 || i % 5 === 0 ? d.date.slice(5) : ""}</div>
                  </div>
                );
              })}
            </div>
          </section>

          <div className="two-col">
            <section className="card">
              <div className="card-head">
                <h2>按模型</h2>
                <Link to="/models" className="link small">
                  模型目录
                </Link>
              </div>
              {a.models.length === 0 ? (
                <div className="empty">区间内没有请求。</div>
              ) : (
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>模型</th>
                        <th className="num">请求</th>
                        <th className="num">输入 / 缓存</th>
                        <th className="num">输出</th>
                        <th className="num">消费</th>
                        {!hidden && <th className="num">毛利</th>}
                        <th className="num">平均延迟</th>
                      </tr>
                    </thead>
                    <tbody>
                      {a.models.map((m) => (
                        <tr key={m.model}>
                          <td className="mono small">{m.model}</td>
                          <td className="num">{fmtNum(m.requests)}</td>
                          <td className="num muted">
                            {fmtNum(m.input_tokens)} / {fmtNum(m.cached_input_tokens)}
                          </td>
                          <td className="num muted">{fmtNum(m.output_tokens)}</td>
                          <td className="num strong">{fmtCNYShort(m.cost_nano)}</td>
                          {!hidden && (
                            <td className={`num ${m.cost_nano - m.provider_cost_nano >= 0 ? "pos" : "neg"}`}>
                              {fmtCNYShort(m.cost_nano - m.provider_cost_nano)}
                            </td>
                          )}
                          <td className="num muted">{m.avg_latency_ms ? `${m.avg_latency_ms}ms` : "—"}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>

            <div className="stack">
              <section className="card">
                <div className="card-head">
                  <h2>按状态</h2>
                </div>
                {a.statuses.length === 0 ? (
                  <div className="empty">区间内没有请求。</div>
                ) : (
                  <div className="usage-list">
                    {a.statuses.map((s) => (
                      <div className="usage-row" key={s.status}>
                        <StatusBadge status={s.status} />
                        <span className="muted small grow">{fmtNum(s.requests)} 次</span>
                        <strong className="small">{fmtCNYShort(s.cost_nano)}</strong>
                      </div>
                    ))}
                  </div>
                )}
              </section>

              <section className="card">
                <div className="card-head">
                  <h2>按密钥</h2>
                  <Link to="/keys" className="link small">
                    管理密钥
                  </Link>
                </div>
                {a.keys.length === 0 ? (
                  <div className="empty">区间内没有请求。</div>
                ) : (
                  <div className="usage-list">
                    {a.keys.map((k) => (
                      <div className="usage-row" key={`${k.api_key_id}-${k.name}`}>
                        <div className="usage-key">
                          <span>{k.name || "未命名密钥"}</span>
                          {(k.prefix || k.suffix) && (
                            <code className="mono muted small">
                              {k.prefix}…{k.suffix}
                            </code>
                          )}
                        </div>
                        <span className="muted small">{fmtNum(k.requests)} 次</span>
                        <strong className="small">{fmtCNYShort(k.cost_nano)}</strong>
                      </div>
                    ))}
                  </div>
                )}
              </section>
            </div>
          </div>
        </>
      )}
    </div>
  );
}

function Stat({ label, value, hint, accent }: { label: string; value: string; hint: string; accent?: boolean }) {
  return (
    <div className={`card stat ${accent ? "accent" : ""}`}>
      <span className="stat-label">{label}</span>
      <strong className="stat-value">{value}</strong>
      <span className="stat-hint">{hint}</span>
    </div>
  );
}
