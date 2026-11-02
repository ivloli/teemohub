import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, fmtCNY, fmtTime, type LedgerEntry, type Summary } from "../api";
import { Icon } from "../components/Icon";
import { PageState } from "../components/PageState";
import { csvStamp, downloadCsv } from "../lib/csv";

type Filter = "all" | "topup" | "usage";

const EXPORT_PAGE = 200;
const EXPORT_MAX = 10000;

const EXPORT_HEADERS = ["ID", "时间", "类型", "金额(CNY)", "余额后(CNY)", "请求 ID", "备注"];

const entryMeta: Record<string, [string, string]> = {
  topup: ["充值", "ok"],
  reserve: ["预留冻结", "muted"],
  settle: ["结算差额", "info"],
  release: ["释放预留", "warn"],
};

export default function Billing() {
  const [balance, setBalance] = useState(0);
  const [entries, setEntries] = useState<LedgerEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [summary, setSummary] = useState<Summary | null>(null);
  const [filter, setFilter] = useState<Filter>("all");
  const [loading, setLoading] = useState(true);
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    Promise.all([
      api<{ balance_nano: number; entries: LedgerEntry[]; total?: number }>("/api/wallet"),
      api<Summary>("/api/usage/summary?days=7").catch(() => null),
    ])
      .then(([w, s]) => {
        setBalance(w.balance_nano || 0);
        setEntries(w.entries || []);
        setTotal(w.total ?? (w.entries || []).length);
        setSummary(s);
      })
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, []);

  const visible = entries.filter((e) =>
    filter === "all" ? true : filter === "topup" ? e.entry_type === "topup" : e.entry_type !== "topup",
  );

  async function exportAll() {
    setExporting(true);
    setError("");
    try {
      const rows: LedgerEntry[] = [];
      for (let off = 0; off < EXPORT_MAX; off += EXPORT_PAGE) {
        const r = await api<{ entries: LedgerEntry[] }>(`/api/wallet?limit=${EXPORT_PAGE}&offset=${off}`);
        const page = r.entries || [];
        rows.push(...page);
        if (page.length < EXPORT_PAGE) break;
      }
      const meta: Record<string, string> = {};
      for (const [k, [label]] of Object.entries(entryMeta)) meta[k] = label;
      downloadCsv(
        `teemohub-ledger-${csvStamp()}.csv`,
        EXPORT_HEADERS,
        rows.map((l) => [
          l.id,
          fmtTime(l.created_at),
          meta[l.entry_type] || l.entry_type,
          (l.amount_nano / 1e8).toFixed(8),
          (l.balance_after_nano / 1e8).toFixed(8),
          l.request_id,
          l.memo,
        ]),
      );
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
          <h1>余额账单</h1>
          <p className="muted">预付费账本：准入预留 → 按实际用量结算差额 → 余额不足拒绝新请求。</p>
        </div>
        <div className="head-actions">
          <button className="btn ghost" onClick={exportAll} disabled={exporting || loading}>
            <Icon name="download" size={16} /> {exporting ? "导出中…" : "导出 CSV"}
          </button>
        </div>
      </header>

      {error && <div className="alert error">{error}</div>}

      <PageState loading={loading} loadingText="加载账单…">
          <div className="two-col">
            <section className="card billing-hero">
              <span className="stat-label">当前余额</span>
              <strong className="billing-balance">{fmtCNY(balance)}</strong>
              <div className="billing-meta">
                <div>
                  <span>今日消费</span>
                  <strong>{fmtCNY(summary?.today_cost_nano || 0)}</strong>
                </div>
                <div>
                  <span>累计消费</span>
                  <strong>{fmtCNY(summary?.total_cost_nano || 0)}</strong>
                </div>
                <div>
                  <span>累计请求</span>
                  <strong>{summary?.total_requests || 0}</strong>
                </div>
              </div>
              <p className="muted small">
                充值由管理员在运营管理中完成；每次扣费都会同时写入请求级账本与余额流水，可在
                <Link to="/logs" className="link"> 请求日志 </Link>
                中逐条核对。
              </p>
            </section>

            <section className="card">
              <div className="card-head">
                <h2>流水类型</h2>
              </div>
              <ol className="ladder">
                <li>
                  <b>充值</b>
                  <span>管理员入账，余额增加</span>
                </li>
                <li>
                  <b>预留冻结</b>
                  <span>请求准入时按预算冻结额度</span>
                </li>
                <li>
                  <b>结算差额</b>
                  <span>实际费用与预留的差额回冲</span>
                </li>
                <li>
                  <b>释放预留</b>
                  <span>未产生输出或上游错误时分文不收</span>
                </li>
              </ol>
            </section>
          </div>

          <section className="card">
            <div className="card-head">
              <h2>余额流水</h2>
              <div className="range-tabs">
                <button className={filter === "all" ? "on" : ""} onClick={() => setFilter("all")}>
                  全部
                </button>
                <button className={filter === "topup" ? "on" : ""} onClick={() => setFilter("topup")}>
                  充值
                </button>
                <button className={filter === "usage" ? "on" : ""} onClick={() => setFilter("usage")}>
                  请求
                </button>
              </div>
            </div>

            {visible.length === 0 ? (
              <div className="empty">暂无流水。</div>
            ) : (
              <>
                <div className="table-wrap">
                  <table>
                    <thead>
                      <tr>
                        <th>时间</th>
                        <th>类型</th>
                        <th className="num">金额</th>
                        <th className="num">余额后</th>
                        <th>备注 / 请求</th>
                      </tr>
                    </thead>
                    <tbody>
                      {visible.map((l) => {
                        const [label, tone] = entryMeta[l.entry_type] || [l.entry_type, "muted"];
                        return (
                          <tr key={l.id}>
                            <td className="muted small nowrap">{fmtTime(l.created_at)}</td>
                            <td>
                              <span className={`badge ${tone}`}>{label}</span>
                            </td>
                            <td className={`num strong ${l.amount_nano < 0 ? "neg" : "pos"}`}>{fmtCNY(l.amount_nano)}</td>
                            <td className="num muted">{fmtCNY(l.balance_after_nano)}</td>
                            <td>
                              {l.request_id ? (
                                <Link to={`/logs/${l.request_id}`} className="mono small link">
                                  {l.request_id}
                                </Link>
                              ) : (
                                <span className="muted small">{l.memo || "—"}</span>
                              )}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
                <div className="table-foot muted small">
                  <Icon name="wallet" size={13} /> 共 {total} 条流水，本页展示最近 {entries.length} 条
                </div>
              </>
            )}
          </section>
      </PageState>
    </div>
  );
}
