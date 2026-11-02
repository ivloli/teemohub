import { useCallback, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import {
  api,
  fmtCNY,
  fmtCNYShort,
  fmtNum,
  fmtTime,
  type Health,
  type RequestRow,
  type Summary,
  type User,
} from "../api";
import { StatusBadge, TrustBadge } from "../components/Badge";
import { Icon } from "../components/Icon";
import { PageState } from "../components/PageState";

export default function Dashboard({ user }: { user: User }) {
  const [summary, setSummary] = useState<Summary | null>(null);
  const [requests, setRequests] = useState<RequestRow[]>([]);
  const [health, setHealth] = useState<Health | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const load = useCallback(async () => {
    setError("");
    try {
      const [s, r, h] = await Promise.all([
        api<Summary>("/api/usage/summary?days=7"),
        api<{ items: RequestRow[] }>("/api/requests?limit=8"),
        fetch("/healthz")
          .then((res) => res.json() as Promise<Health>)
          .catch(() => null),
      ]);
      setSummary(s);
      setRequests(r.items || []);
      setHealth(h);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const daily = summary?.daily || [];
  const maxCost = Math.max(1, ...daily.map((d) => d.cost_nano));

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>概览</h1>
          <p className="muted">
            {new Date().toLocaleDateString("zh-CN", { month: "long", day: "numeric", weekday: "long" })} · 欢迎回来，{user.display_name}
          </p>
        </div>
        <div className="head-actions">
          <div className="health-strip">
            {health?.checks?.length ? (
              health.checks.map((c) => (
                <span
                  key={c.name}
                  className={`health-chip ${c.ok ? "ok" : "bad"}`}
                  title={[c.detail, c.error].filter(Boolean).join(" · ")}
                >
                  <i /> {checkName(c.name)}
                  <em>{c.latency_ms}ms</em>
                </span>
              ))
            ) : (
              <span className={`health-chip ${health?.ok ? "ok" : "bad"}`}>
                <i /> {health?.ok ? "服务正常" : "健康检查不可用"}
              </span>
            )}
          </div>
          <button className="btn ghost" onClick={load}>
            <Icon name="refresh" size={16} /> 刷新
          </button>
          <Link className="btn primary" to="/playground">
            <Icon name="play" size={16} /> 开始体验
          </Link>
        </div>
      </header>

      {error && <div className="alert error">{error}</div>}
      <PageState loading={loading} loadingText="加载中…">
          <section className="stat-grid">
            <Stat label="账户余额" value={fmtCNY(user.balance_nano)} hint="CNY · 实时结算后余额" accent />
            <Stat label="今日消费" value={fmtCNY(summary?.today_cost_nano || 0)} hint={`${summary?.today_requests || 0} 次请求`} />
            <Stat
              label="今日 Tokens"
              value={fmtNum((summary?.today_input_tokens || 0) + (summary?.today_output_tokens || 0))}
              hint={`入 ${fmtNum(summary?.today_input_tokens || 0)} · 出 ${fmtNum(summary?.today_output_tokens || 0)}`}
            />
            <Stat label="累计请求" value={fmtNum(summary?.total_requests || 0)} hint={`累计消费 ${fmtCNYShort(summary?.total_cost_nano || 0)}`} />
          </section>

          <div className="two-col">
            <section className="card chart-card">
              <div className="card-head">
                <h2>近 7 日消费</h2>
                <span className="muted small">按结算金额</span>
              </div>
              <div className="bars">
                {daily.map((d) => (
                  <div className="bar-col" key={d.date} title={`${d.date} · ${fmtCNY(d.cost_nano)} · ${d.requests} 次`}>
                    <div className="bar-value">{d.cost_nano ? fmtCNYShort(d.cost_nano) : ""}</div>
                    <div className="bar-track">
                      <div
                        className={`bar-fill ${d.cost_nano === maxCost && d.cost_nano > 0 ? "peak" : ""}`}
                        style={{ height: `${Math.max(d.cost_nano ? 4 : 0, (d.cost_nano / maxCost) * 100)}%` }}
                      />
                    </div>
                    <div className="bar-label">{d.date.slice(5)}</div>
                  </div>
                ))}
              </div>
            </section>

            <section className="card quick-card">
              <div className="card-head">
                <h2>快速开始</h2>
              </div>
              <ol className="steps">
                <li>
                  <span className="step-no">1</span>
                  <div>
                    在 <Link to="/keys">API 密钥</Link> 创建 <code>sk-th-…</code>，用于访问网关。
                  </div>
                </li>
                <li>
                  <span className="step-no">2</span>
                  <div>
                    在 <Link to="/models">模型目录</Link> 对比输入 / 缓存 / 输出价格与上下文长度。
                  </div>
                </li>
                <li>
                  <span className="step-no">3</span>
                  <div>
                    在 <Link to="/playground">体验场</Link> 粘贴密钥开始对话；随时点击 Stop 验证断流结算。
                  </div>
                </li>
                <li>
                  <span className="step-no">4</span>
                  <div>
                    在 <Link to="/logs">请求日志</Link> 核对逐条用量与账本，在 <Link to="/usage">用量分析</Link> 查看趋势。
                  </div>
                </li>
              </ol>
              <div className="endpoint">
                <span className="muted small">OpenAI 兼容端点</span>
                <code>POST /v1/chat/completions</code>
              </div>
            </section>
          </div>

          <section className="card">
            <div className="card-head">
              <h2>最近请求</h2>
              <div className="head-links">
                <Link to="/usage" className="link small">
                  用量分析
                </Link>
                <Link to="/logs" className="link">
                  查看全部
                </Link>
              </div>
            </div>
            {requests.length === 0 ? (
              <Empty text="还没有请求。创建密钥后到体验场发起第一次对话。" />
            ) : (
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>时间</th>
                      <th>模型</th>
                      <th>状态</th>
                      <th>用量来源</th>
                      <th className="num">输入</th>
                      <th className="num">输出</th>
                      <th className="num">费用</th>
                      <th className="num">延迟</th>
                    </tr>
                  </thead>
                  <tbody>
                    {requests.map((r) => (
                      <tr key={r.id}>
                        <td className="muted small">{fmtTime(r.created_at)}</td>
                        <td>
                          <Link to={`/logs/${r.id}`} className="mono">
                            {r.model}
                          </Link>
                        </td>
                        <td>
                          <StatusBadge status={r.status} />
                        </td>
                        <td>
                          <TrustBadge source={r.usage_source} />
                        </td>
                        <td className="num">{fmtNum(r.input_tokens)}</td>
                        <td className="num">{fmtNum(r.output_tokens)}</td>
                        <td className="num strong">{fmtCNY(r.cost_nano)}</td>
                        <td className="num muted">{r.latency_ms}ms</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </section>
      </PageState>
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

export function Empty({ text }: { text: string }) {
  return <div className="empty">{text}</div>;
}

function checkName(name: string): string {
  switch (name) {
    case "database":
      return "存储";
    case "new_api":
      return "New API";
    case "metering":
      return "计量";
    default:
      return name;
  }
}
