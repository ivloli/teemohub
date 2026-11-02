import { useEffect, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, fmtCNY, fmtCNYPerM, fmtNum, type PublicModel, type RequestRow } from "../api";
import { StatusBadge, TrustBadge } from "../components/Badge";
import { Icon } from "../components/Icon";

type ChatMessage = {
  role: "user" | "assistant";
  content: string;
  reasoning?: string;
  tools?: string;
};

type Phase = "idle" | "streaming" | "settling";

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

export default function Playground() {
  const [params] = useSearchParams();
  const preset = params.get("model") || "";
  const [models, setModels] = useState<PublicModel[]>([]);
  const [model, setModel] = useState("");
  const [key, setKey] = useState(() => localStorage.getItem("th_key") || "");
  const [system, setSystem] = useState("");
  const [showSystem, setShowSystem] = useState(false);
  const [input, setInput] = useState("");
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [phase, setPhase] = useState<Phase>("idle");
  const [bill, setBill] = useState<RequestRow | null>(null);
  const [requestID, setRequestID] = useState("");
  const [traceID, setTraceID] = useState("");
  const [error, setError] = useState("");
  const [liveChars, setLiveChars] = useState(0);

  const abortRef = useRef<AbortController | null>(null);
  const bottomRef = useRef<HTMLDivElement | null>(null);
  const stoppedRef = useRef(false);

  useEffect(() => {
    api<{ items: PublicModel[] }>("/api/models")
      .then((r) => {
        const list = (r.items || []).filter((m) => m.enabled);
        setModels(list);
        if (!list.length) return;
        const match = list.find((m) => m.id === preset);
        setModel((cur) => (cur && list.some((m) => m.id === cur) ? cur : match ? match.id : list[0].id));
      })
      .catch((e) => setError((e as Error).message));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [messages, phase]);

  useEffect(() => {
    if (key) localStorage.setItem("th_key", key);
  }, [key]);

  const current = models.find((m) => m.id === model);

  async function pollBill(id: string) {
    for (let i = 0; i < 60; i++) {
      try {
        const d = await api<{ request: RequestRow }>(`/api/requests/${id}`);
        setBill(d.request);
        if (d.request.status !== "reserved" && d.request.status !== "streaming") return;
      } catch {
        return;
      }
      await sleep(400);
    }
  }

  async function send() {
    const text = input.trim();
    if (!text || phase !== "idle") return;
    if (!key.startsWith("sk-th-")) {
      setError("请先在左侧填入以 sk-th- 开头的 teemoHub 密钥。");
      return;
    }
    if (!model) {
      setError("暂无可用模型。");
      return;
    }
    setError("");
    stoppedRef.current = false;
    setBill(null);
    setRequestID("");
    setTraceID("");
    setLiveChars(0);
    setInput("");

    const history = [...messages, { role: "user" as const, content: text }];
    setMessages([...history, { role: "assistant", content: "" }]);
    setPhase("streaming");

    const headerMessages: { role: string; content: string }[] = [];
    if (system.trim()) headerMessages.push({ role: "system", content: system.trim() });
    for (const m of history) headerMessages.push({ role: m.role, content: m.content });

    const ctrl = new AbortController();
    abortRef.current = ctrl;
    let rid = "";
    try {
      const res = await fetch("/v1/chat/completions", {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${key}` },
        body: JSON.stringify({ model, messages: headerMessages, stream: true, max_tokens: 2048 }),
        signal: ctrl.signal,
      });
      rid = res.headers.get("X-TeemoHub-Request-Id") || "";
      const trace = res.headers.get("X-TeemoHub-Trace-Id") || "";
      if (rid) setRequestID(rid);
      if (trace) setTraceID(trace);

      if (!res.ok) {
        const body = await res.text();
        let msg = body;
        try {
          msg = JSON.parse(body)?.error?.message || body;
        } catch {
          /* noop */
        }
        throw new Error(msg || `HTTP ${res.status}`);
      }

      let content = "";
      let reasoning = "";
      let tools = "";
      let chars = 0;
      const reader = res.body?.getReader();
      if (reader) {
        const dec = new TextDecoder();
        let buf = "";
        while (true) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += dec.decode(value, { stream: true });
          const lines = buf.split("\n");
          buf = lines.pop() ?? "";
          for (const raw of lines) {
            const line = raw.replace(/\r$/, "").trim();
            if (!line.startsWith("data:")) continue;
            const payload = line.slice(5).trim();
            if (!payload || payload === "[DONE]") continue;
            try {
              const chunk = JSON.parse(payload);
              for (const choice of chunk.choices || []) {
                const delta = choice.delta || {};
                if (delta.content) {
                  content += delta.content;
                  chars += delta.content.length;
                }
                if (delta.reasoning_content) reasoning += delta.reasoning_content;
                if (delta.reasoning) reasoning += delta.reasoning;
                for (const tc of delta.tool_calls || []) {
                  const name = tc.function?.name || "";
                  const args = tc.function?.arguments || "";
                  tools += `${name ? `\n[${name}] ` : ""}${args}`;
                }
              }
            } catch {
              /* partial or non-JSON line */
            }
            setLiveChars(chars);
            setMessages((prev) => {
              const next = [...prev];
              next[next.length - 1] = { role: "assistant", content, reasoning, tools };
              return next;
            });
          }
        }
      }
      setMessages((prev) => {
        const next = [...prev];
        next[next.length - 1] = { role: "assistant", content, reasoning, tools };
        return next;
      });
    } catch (e) {
      const err = e as Error;
      if (!stoppedRef.current) {
        setError(err.name === "AbortError" ? "" : err.message);
      }
      if (!rid && stoppedRef.current) {
        setError("请求在拿到响应头前被取消。");
      }
    } finally {
      abortRef.current = null;
      if (rid) {
        setPhase("settling");
        await pollBill(rid);
      }
      setPhase("idle");
    }
  }

  function stop() {
    stoppedRef.current = true;
    abortRef.current?.abort();
  }

  function reset() {
    if (phase !== "idle") return;
    setMessages([]);
    setBill(null);
    setRequestID("");
    setTraceID("");
    setError("");
  }

  return (
    <div className="page playground">
      <header className="page-head">
        <div>
          <h1>体验场</h1>
          <p className="muted">
            流式对话 · 随时 Stop 验证断流计费 · 每次响应后展示用量来源与结算
          </p>
        </div>
        <div className="head-actions">
          <button className="btn ghost" onClick={reset} disabled={phase !== "idle" || !messages.length}>
            清空对话
          </button>
        </div>
      </header>

      {error && <div className="alert error">{error}</div>}

      <div className="play-grid">
        <div className="chat-col">
          <section className="card chat-card">
            <div className="chat-toolbar">
              <label className="field grow">
                <span>模型</span>
                <select value={model} onChange={(e) => setModel(e.target.value)} disabled={phase !== "idle"}>
                  {models.map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.display_name} · {m.id}
                    </option>
                  ))}
                </select>
              </label>
              <label className="field grow key-field">
                <span>teemoHub 密钥</span>
                <input
                  type="password"
                  value={key}
                  onChange={(e) => setKey(e.target.value.trim())}
                  placeholder="sk-th-…"
                  disabled={phase !== "idle"}
                />
              </label>
            </div>

            {showSystem ? (
              <div className="system-box">
                <span>系统提示</span>
                <textarea rows={2} value={system} onChange={(e) => setSystem(e.target.value)} placeholder="可选，例如：你是简洁的中文助手。" />
              </div>
            ) : (
              <button className="link small system-toggle" onClick={() => setShowSystem(true)}>
                + 添加系统提示
              </button>
            )}

            <div className="messages">
              {messages.length === 0 && (
                <div className="chat-empty">
                  <div className="chat-empty-mark">T</div>
                  <h3>干净的一次调用</h3>
                  <p>输入任意问题开始。流式输出过程中点击 Stop，服务端会在预算内继续 drain，随后按用量快照 + 本地补算结算。</p>
                  {current && (
                    <div className="price-note">
                      当前模型：入 {fmtCNYPerM(current.input_price_nano)} · 缓存入 {fmtCNYPerM(current.cached_input_price_nano)} · 出 {fmtCNYPerM(current.output_price_nano)}
                    </div>
                  )}
                </div>
              )}
              {messages.map((m, i) => (
                <div className={`msg ${m.role}`} key={i}>
                  <div className="msg-role">{m.role === "user" ? "你" : "助手"}</div>
                  <div className="msg-body">
                    {m.reasoning && (
                      <details className="reasoning">
                        <summary>思考过程</summary>
                        <pre>{m.reasoning}</pre>
                      </details>
                    )}
                    {m.content ? <div className="msg-text">{m.content}</div> : m.role === "assistant" && phase === "streaming" ? <Typing /> : null}
                    {m.tools && (
                      <details className="tools" open>
                        <summary>Tool calls</summary>
                        <pre>{m.tools.trim()}</pre>
                      </details>
                    )}
                  </div>
                </div>
              ))}
              <div ref={bottomRef} />
            </div>

            <div className="composer">
              <textarea
                rows={2}
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && !e.shiftKey) {
                    e.preventDefault();
                    send();
                  }
                }}
                placeholder="输入消息，Enter 发送，Shift+Enter 换行"
                disabled={phase !== "idle"}
              />
              {phase === "streaming" ? (
                <button className="btn danger stop-btn" onClick={stop}>
                  <Icon name="stop" size={16} /> Stop
                </button>
              ) : phase === "settling" ? (
                <button className="btn" disabled>
                  结算中…
                </button>
              ) : (
                <button className="btn primary" onClick={send} disabled={!input.trim() || !key}>
                  <Icon name="play" size={16} /> 发送
                </button>
              )}
            </div>
            {phase === "streaming" && (
              <div className="stream-foot">
                <span className="pulse-dot" /> 接收中 · 已收到 {liveChars} 字符 · 点击 Stop 可测试断流结算
              </div>
            )}
          </section>
        </div>

        <aside className="bill-col">
          <section className="card bill-card">
            <div className="card-head">
              <h2>本次结算</h2>
              {bill ? <StatusBadge status={bill.status} /> : <span className="badge muted">等待请求</span>}
            </div>
            {!bill && (
              <div className="bill-empty">
                {phase === "settling" ? (
                  <span className="muted">等待服务端结算并写入账本…</span>
                ) : (
                  <span className="muted">发送一条消息后，这里会展示 token 用量、计费来源与账本。</span>
                )}
              </div>
            )}
            {bill && (
              <>
                <div className="bill-hero">
                  <span className="bill-label">本次费用</span>
                  <strong className="bill-amount">{fmtCNY(bill.cost_nano)}</strong>
                  <div className="bill-sub">
                    <TrustBadge source={bill.usage_source} />
                    <span className="muted small">trust: {bill.usage_trust}</span>
                  </div>
                </div>
                <dl className="kv">
                  <div>
                    <dt>输入 tokens</dt>
                    <dd>{fmtNum(bill.input_tokens)}</dd>
                  </div>
                  <div>
                    <dt>输出 tokens</dt>
                    <dd>{fmtNum(bill.output_tokens)}</dd>
                  </div>
                  <div>
                    <dt>缓存命中 tokens</dt>
                    <dd>{fmtNum(bill.cached_input_tokens)}</dd>
                  </div>
                  <div>
                    <dt>推理 tokens</dt>
                    <dd>{fmtNum(bill.reasoning_tokens)}</dd>
                  </div>
                  <div>
                    <dt>首 token</dt>
                    <dd>{bill.first_token_ms ? `${bill.first_token_ms}ms` : "-"}</dd>
                  </div>
                  <div>
                    <dt>总延迟</dt>
                    <dd>{bill.latency_ms}ms</dd>
                  </div>
                  <div>
                    <dt>供应商成本</dt>
                    <dd>{fmtCNY(bill.provider_cost_nano)}</dd>
                  </div>
                  <div>
                    <dt>预留</dt>
                    <dd>{fmtCNY(bill.reserved_nano)}</dd>
                  </div>
                  <div>
                    <dt>断流</dt>
                    <dd>{bill.client_disconnected ? "是" : "否"}</dd>
                  </div>
                </dl>
                <div className="source-note">{sourceNote(bill.usage_source)}</div>
                <div className="bill-ids">
                  <div>
                    <span>request</span>
                    <code>{bill.id}</code>
                  </div>
                  <div>
                    <span>trace</span>
                    <code>{bill.trace_id}</code>
                  </div>
                </div>
                <Link className="btn ghost block" to={`/logs/${bill.id}`}>
                  查看完整用量与账本
                </Link>
              </>
            )}
          </section>

          <section className="card">
            <div className="card-head">
              <h2>计费阶梯</h2>
            </div>
            <ol className="ladder">
              <li className={bill?.usage_source === "provider_final" ? "on" : ""}>
                <b>provider_final</b>
                <span>供应商终值，校准金标准</span>
              </li>
              <li className={bill?.usage_source === "provider_partial" ? "on" : ""}>
                <b>provider_partial</b>
                <span>快照锚定 + 本地尾段补算</span>
              </li>
              <li className={bill?.usage_source === "counted_text" || bill?.usage_source === "estimate_text" ? "on" : ""}>
                <b>local_estimate</b>
                <span>全文本地计数 × 校准系数</span>
              </li>
              <li className={bill?.usage_source === "none" ? "on" : ""}>
                <b>none</b>
                <span>无首 token / 上游错误，不计费</span>
              </li>
            </ol>
          </section>
        </aside>
      </div>
    </div>
  );
}

function Typing() {
  return (
    <span className="typing">
      <i /> <i /> <i />
    </span>
  );
}

function sourceNote(source: string): string {
  switch (source) {
    case "provider_final":
      return "上游返回了完整 usage（通常位于最后一个 chunk）。按供应商终值计费，并写入校准样本。";
    case "provider_partial":
      return "已收到中途 usage 快照，但连接提前结束。以快照为锚点，加上快照之后的本地输出估算。";
    case "counted_text":
      return "未收到供应商 usage：按本次完整输出文本本地计数，乘以该校准系数后计费。";
    case "estimate_text":
      return "未收到供应商 usage：按输入与已收到的输出文本本地估算。";
    case "estimate_prompt_only":
      return "只拿到提示词侧估算，输出侧没有可靠信号。";
    case "none":
      return "上游错误或没有产生任何输出，本次不计费，预留额度已释放。";
    default:
      return "";
  }
}
