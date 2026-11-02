import { useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { api, fmtCNYShort, type User } from "./api";
import Login from "./pages/Login";
import Dashboard from "./pages/Dashboard";
import Models from "./pages/Models";
import Usage from "./pages/Usage";
import Billing from "./pages/Billing";
import Keys from "./pages/Keys";
import Playground from "./pages/Playground";
import Logs from "./pages/Logs";
import Admin from "./pages/Admin";
import { Icon } from "./components/Icon";
import { Spinner } from "./components/Spinner";

export default function App() {
  const [user, setUser] = useState<User | null | undefined>(undefined);

  useEffect(() => {
    api<{ user: User }>("/api/me")
      .then((r) => setUser(r.user))
      .catch(() => setUser(null));
  }, []);

  if (user === undefined) {
    return (
      <div className="boot">
        <Spinner />
      </div>
    );
  }
  if (user === null) {
    return <Login onLogin={setUser} />;
  }

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <div className="brand-mark">T</div>
          <div>
            <div className="brand-name">teemoHub</div>
            <div className="brand-sub">AI Gateway Console</div>
          </div>
        </div>

        <nav className="nav">
          <NavLink to="/" end>
            <Icon name="grid" /> <span>概览</span>
          </NavLink>

          <div className="nav-group">
            <div className="nav-group-label">探索</div>
            <NavLink to="/models">
              <Icon name="cube" /> <span>模型目录</span>
            </NavLink>
            <NavLink to="/playground">
              <Icon name="chat" /> <span>体验场</span>
            </NavLink>
          </div>

          <div className="nav-group">
            <div className="nav-group-label">用量与账单</div>
            <NavLink to="/logs">
              <Icon name="list" /> <span>请求日志</span>
            </NavLink>
            <NavLink to="/usage">
              <Icon name="chart" /> <span>用量分析</span>
            </NavLink>
            <NavLink to="/billing">
              <Icon name="wallet" /> <span>余额账单</span>
            </NavLink>
            <NavLink to="/keys">
              <Icon name="key" /> <span>API 密钥</span>
            </NavLink>
          </div>

          {user.role === "admin" && (
            <div className="nav-group">
              <div className="nav-group-label">系统</div>
              <NavLink to="/admin">
                <Icon name="sliders" /> <span>运营管理</span>
              </NavLink>
            </div>
          )}
        </nav>

        <div className="sidebar-foot">
          <div className="balance-chip">
            <span>余额</span>
            <strong>{fmtCNYShort(user.balance_nano)}</strong>
          </div>
          <div className="account">
            <div className="avatar">{user.display_name.slice(0, 1).toUpperCase()}</div>
            <div className="account-meta">
              <div>{user.display_name}</div>
              <span>{user.role === "admin" ? "管理员" : "用户"} · {user.username}</span>
            </div>
            <button
              className="icon-btn"
              title="退出登录"
              onClick={async () => {
                await api("/api/auth/logout", { method: "POST" }).catch(() => undefined);
                location.reload();
              }}
            >
              <Icon name="logout" />
            </button>
          </div>
        </div>
      </aside>

      <main className="main">
        <Routes>
          <Route path="/" element={<Dashboard user={user} />} />
          <Route path="/models" element={<Models />} />
          <Route path="/playground" element={<Playground />} />
          <Route path="/keys" element={<Keys />} />
          <Route path="/logs" element={<Logs />} />
          <Route path="/logs/:id" element={<Logs />} />
          <Route path="/usage" element={<Usage />} />
          <Route path="/billing" element={<Billing />} />
          <Route path="/admin" element={user.role === "admin" ? <Admin /> : <Navigate to="/" replace />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </div>
  );
}
