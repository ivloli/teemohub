import { useEffect, useState } from "react";
import { api, fmtTime, type APIKey } from "../api";
import { Icon } from "../components/Icon";
import { PageState } from "../components/PageState";

export default function Keys() {
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [loading, setLoading] = useState(true);
  const [name, setName] = useState("default");
  const [rpm, setRpm] = useState(60);
  const [tpm, setTpm] = useState(0);
  const [concurrent, setConcurrent] = useState(0);
  const [maxOutput, setMaxOutput] = useState(0);
  const [secret, setSecret] = useState<{ key: APIKey; secret: string } | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [copied, setCopied] = useState(false);
  const [savedHint, setSavedHint] = useState(0);

  async function load() {
    setLoading(true);
    try {
      const r = await api<{ items: APIKey[] }>("/api/keys");
      setKeys(r.items || []);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, []);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api<{ key: APIKey; secret: string }>("/api/keys", {
        method: "POST",
        body: JSON.stringify({ name, rpm, tpm, concurrent, max_output: maxOutput }),
      });
      setSecret(r);
      setCopied(false);
      await load();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function revoke(k: APIKey) {
    if (!confirm(`撤销密钥 ${k.prefix}…${k.suffix}？使用该密钥的客户端将立即失效。`)) return;
    try {
      await api(`/api/keys/${k.id}`, { method: "DELETE" });
      await load();
    } catch (e) {
      setError((e as Error).message);
    }
  }

  function useInPlayground(s: string) {
    localStorage.setItem("th_key", s);
    setSavedHint(Date.now());
  }

  return (
    <div className="page">
      <header className="page-head">
        <div>
          <h1>密钥</h1>
          <p className="muted">网关使用 <code>sk-th-</code> 前缀的密钥鉴权；明文仅在创建时展示一次。</p>
        </div>
      </header>

      {error && <div className="alert error">{error}</div>}

      <div className="two-col keys-layout">
        <section className="card">
          <div className="card-head">
            <h2>新建密钥</h2>
          </div>
          <form className="form" onSubmit={create}>
            <label>
              名称
              <input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如 laptop / server" />
            </label>
            <label>
              RPM 限制
              <input type="number" min={1} max={10000} value={rpm} onChange={(e) => setRpm(Number(e.target.value))} />
            </label>
            <div className="form-row">
              <label>
                TPM（0=不限）
                <input type="number" min={0} value={tpm} onChange={(e) => setTpm(Number(e.target.value))} />
              </label>
              <label>
                并发（0=默认）
                <input type="number" min={0} max={64} value={concurrent} onChange={(e) => setConcurrent(Number(e.target.value))} />
              </label>
              <label>
                输出上限（0=不限）
                <input type="number" min={0} value={maxOutput} onChange={(e) => setMaxOutput(Number(e.target.value))} />
              </label>
            </div>
            <button className="btn primary" disabled={busy || !name}>
              <Icon name="plus" size={16} /> {busy ? "创建中…" : "创建密钥"}
            </button>
          </form>
          <div className="hint-box">
            <strong>安全提示</strong>
            <p>密钥只在创建时返回一次；服务端仅保存 SHA-256 摘要。丢失后请撤销并重建。</p>
          </div>
        </section>

        <section className="card">
          <div className="card-head">
            <h2>我的密钥</h2>
            <span className="muted small">{keys.filter((k) => k.status === 1).length} 个可用</span>
          </div>
          <PageState loading={loading} empty={keys.length === 0} emptyText="还没有密钥，先在左侧创建一个。">
            <div className="key-list">
              {keys.map((k) => (
                <div className={`key-row ${k.status !== 1 ? "revoked" : ""}`} key={k.id}>
                  <div className="key-icon">
                    <Icon name="key" />
                  </div>
                  <div className="key-main">
                    <div className="key-title">
                      {k.name}
                      <span className={`badge ${k.status === 1 ? "ok" : "muted"}`}>{k.status === 1 ? "可用" : "已撤销"}</span>
                    </div>
                    <code className="mono muted">
                      {k.prefix}…{k.suffix}
                    </code>
                    <div className="key-meta">
                      {k.rpm} RPM · TPM {k.tpm || "不限"} · 并发 {k.concurrent || "默认"} · 输出 ≤ {k.max_output || "不限"} · 创建于{" "}
                      {fmtTime(k.created_at)} · 最近使用 {k.last_used_at ? fmtTime(k.last_used_at) : "从未"}
                    </div>
                  </div>
                  {k.status === 1 && (
                    <button className="btn ghost danger" onClick={() => revoke(k)}>
                      撤销
                    </button>
                  )}
                </div>
              ))}
            </div>
          </PageState>
        </section>
      </div>

      {secret && (
        <div className="modal-backdrop" onClick={() => setSecret(null)}>
          <div className="modal" onClick={(e) => e.stopPropagation()}>
            <h2>密钥已创建</h2>
            <p className="muted">这是唯一一次展示完整密钥，请立即保存到安全位置。</p>
            <div className="secret-box">
              <code>{secret.secret}</code>
              <button
                className="btn ghost"
                onClick={async () => {
                  await navigator.clipboard.writeText(secret.secret);
                  setCopied(true);
                }}
              >
                <Icon name="copy" size={16} /> {copied ? "已复制" : "复制"}
              </button>
            </div>
            <div className="modal-actions">
              <button
                className="btn"
                onClick={() => {
                  useInPlayground(secret.secret);
                  setSecret(null);
                }}
              >
                填入体验场
              </button>
              <button className="btn primary" onClick={() => setSecret(null)}>
                我已保存
              </button>
            </div>
          </div>
        </div>
      )}

      {savedHint > 0 && <div className="toast">已写入浏览器本地，体验场将自动使用该密钥</div>}
    </div>
  );
}
