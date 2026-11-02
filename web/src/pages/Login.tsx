import { useState } from "react";
import { api, type User } from "../api";
import { Icon } from "../components/Icon";

export default function Login({ onLogin }: { onLogin: (u: User) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api<{ user: User }>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ username, password }),
      });
      onLogin(r.user);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="login">
      <section className="login-hero">
        <div className="hero-brand">
          <div className="brand-mark big">T</div>
          <span>teemoHub</span>
        </div>
        <h1>
          统一接入。
          <br />
          每一枚 token 都算得清。
        </h1>
        <p>
          自建 AI 网关，前置 New API 与多家模型供应商。按请求计量、精确结算，
          客户端中断也能得到可信的账单。
        </p>
        <ul className="hero-points">
          <li>
            <Icon name="bolt" /> 流式转发与断流计费
          </li>
          <li>
            <Icon name="shield" /> 密钥托管、限流与预算
          </li>
          <li>
            <Icon name="list" /> 请求级用量溯源与账本
          </li>
        </ul>
        <div className="hero-glow" />
      </section>

      <section className="login-panel">
        <form className="login-card" onSubmit={submit}>
          <h2>登录控制台</h2>
          <p className="muted">使用 teemoHub 账号进入</p>
          <label>
            用户名
            <input
              autoFocus
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              placeholder="admin"
              autoComplete="username"
            />
          </label>
          <label>
            密码
            <input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="••••••••"
              autoComplete="current-password"
            />
          </label>
          {error && <div className="alert error">{error}</div>}
          <button className="btn primary block" disabled={busy || !username || !password}>
            {busy ? "登录中…" : "登录"}
          </button>
          <div className="login-note">
            账号由管理员创建，密码保存在本机 SQLite 中，不经过外部服务。
          </div>
        </form>
      </section>
    </div>
  );
}
