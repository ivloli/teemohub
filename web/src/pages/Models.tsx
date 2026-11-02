import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { api, fmtCNYPerM, fmtNum, type PublicModel } from "../api";
import { Icon } from "../components/Icon";
import { PageState } from "../components/PageState";

export default function Models() {
  const [models, setModels] = useState<PublicModel[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [q, setQ] = useState("");

  useEffect(() => {
    api<{ items: PublicModel[] }>("/api/models")
      .then((r) => setModels(r.items || []))
      .catch((e) => setError((e as Error).message))
      .finally(() => setLoading(false));
  }, []);

  const needle = q.trim().toLowerCase();
  const items = needle
    ? models.filter(
        (m) =>
          m.id.toLowerCase().includes(needle) ||
          m.display_name.toLowerCase().includes(needle) ||
          m.family.toLowerCase().includes(needle),
      )
    : models;

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>模型目录</h1>
          <p className="muted">OpenAI 兼容端点 · 价格按 ¥/百万 tokens 展示 · 缓存命中自动使用折扣价。</p>
        </div>
        <div className="head-actions">
          <label className="field search-field">
            <Icon name="search" size={15} />
            <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="搜索模型 / 家族" />
          </label>
        </div>
      </header>

      {error && <div className="alert error">{error}</div>}

      <PageState
        loading={loading}
        loadingText="加载模型…"
        empty={items.length === 0}
        emptyText={needle ? "没有匹配的模型。" : "暂无可用模型，请联系管理员在运营管理中启用。"}
      >
          <section className="model-grid">
            {items.map((m) => (
              <article className="card model-card" key={m.id}>
                <div className="model-card-head">
                  <div className="model-title">
                    <strong>{m.display_name}</strong>
                    <code className="mono muted model-id">{m.id}</code>
                  </div>
                  <span className={`badge ${m.enabled ? "ok" : "muted"}`}>{m.enabled ? "可用" : "停用"}</span>
                </div>

                <div className="model-prices">
                  <div className="price-cell">
                    <span>输入</span>
                    <strong>{fmtCNYPerM(m.input_price_nano)}</strong>
                  </div>
                  <div className="price-cell">
                    <span>缓存输入</span>
                    <strong>{m.cached_input_price_nano ? fmtCNYPerM(m.cached_input_price_nano) : "同输入价"}</strong>
                  </div>
                  <div className="price-cell">
                    <span>输出</span>
                    <strong>{fmtCNYPerM(m.output_price_nano)}</strong>
                  </div>
                </div>

                <div className="model-meta">
                  <span>{m.family || "通用"}</span>
                  <span>上下文 {fmtNum(m.context_window)}</span>
                  <span>最大输出 {fmtNum(m.max_output)}</span>
                </div>

                <div className="model-actions">
                  <Link className="btn primary grow" to={`/playground?model=${encodeURIComponent(m.id)}`}>
                    <Icon name="play" size={16} /> 在体验场试用
                  </Link>
                  <Link className="btn ghost" to={`/logs?model=${encodeURIComponent(m.id)}`}>
                    查看日志
                  </Link>
                </div>
              </article>
            ))}
          </section>

          <section className="card note-card">
            <div className="card-head">
              <h2>计费口径</h2>
            </div>
            <ul className="note-list">
              <li>
                <b>缓存输入</b>
                <span>上游返回 prompt cache 命中时，命中部分按缓存价计费，其余按输入价计费；未返回命中信息时全部按输入价。</span>
              </li>
              <li>
                <b>用量来源</b>
                <span>优先采用供应商 usage 终值；断流时使用快照锚定 + 本地补算，明细可在请求日志中逐条核对。</span>
              </li>
              <li>
                <b>调用方式</b>
                <span>
                  将 <code>base_url</code> 指向网关的 <code>/v1</code>，使用 <code>sk-th-</code> 密钥即可，兼容 OpenAI SDK。
                </span>
              </li>
            </ul>
          </section>
      </PageState>
    </div>
  );
}
