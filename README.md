# teemoHub

本地版本的 Token Rhythm 式 AI 网关：登录 → 发 Key → 网关 → New API → DeepSeek/Kimi → 计量计费 → 前端看账。
New API 保持原样，teemoHub 负责多租户、限流、断流计费与可视化。

## 架构

```text
Browser (React) ──► teemoHub control :18080  (登录/Key/日志/余额/管理台)
                         │
                         └─► teemoHub gateway :18081 /v1/chat/completions
                                  │  detached upstream + drain
                                  ▼
                              New API :3000  ──► DeepSeek / Kimi
```

- 双服务单进程：`cmd/teemohub` 同时监听控制面与数据面。
- SQLite（WAL）：`runtime/teemohub.db`，记录请求、终态 usage 事件与复式账本。
- 计费阶梯：`provider_final > provider_partial > counted_text > estimate_text > estimate_prompt_only > none`。
- 客户端断开后不立即掐上游：按 `TEEMOHUB_DRAIN_*` 预算继续读，拿不到终态 usage 时用“本地计数 × 校准系数”结算。

## 快速开始

```bash
cp .env.example .env       # 填 DEEPSEEK_API_KEY / KIMI_API_KEY / 管理员密码，chmod 600
make newapi                # 构建并启动 New API :3000
make bootstrap             # 初始化 root、渠道、teemoHub 上游 Token
make web                   # 构建前端到 web/dist
make up                    # 启动 teemoHub :18080/:18081
```

打开 <http://127.0.0.1:18080>，用 `.env` 中的管理员账号登录，在「Keys」创建 API Key，
即可在「Playground」或任意 OpenAI SDK 中使用：

```bash
curl http://127.0.0.1:18081/v1/chat/completions \
  -H "Authorization: Bearer sk-th-..." \
  -H "Content-Type: application/json" \
  -d '{"model":"deepseek-chat","stream":true,"messages":[{"role":"user","content":"hello"}]}'
```

响应头 `X-TeemoHub-Request-Id` 可在「Logs」中查到该请求的 usage 来源、token 与账本流水。

## 测试

```bash
make e2e                   # 需要两个服务的真实上游可用
make limits                # 并发上限 / RPM / TPM 与单次结算
make persist               # make restart 后余额、账本、请求历史不变
make audit                 # 价格版本化快照、审计事件与 append-only 触发器
make reconcile             # 与 New API 消耗日志按日对账
```

覆盖：非流式、流式、0.5/1/2/5s 主动断流、双工具调用、JSON mode、reasoning、Kimi、非法请求/Key/模型，
并逐条断言账本平衡与费用公式。报告分别写入 `runtime/e2e-report.json`、`runtime/limits-report.json`、
`runtime/persistence-report.json`、`runtime/audit-report.json`。

## 目录

```text
cmd/teemohub/        入口（control + gateway）
internal/config/     环境变量
internal/store/      SQLite、账本、结算
internal/api/        控制面与数据面 HTTP
internal/meter/      tokenizer 启发式、计费、校准
internal/upstream/   New API 客户端
web/                 React + Vite 前端
scripts/             New API 启动/初始化、E2E
docs/teemohub-frd.md 需求与实测记录
```

## 安全与边界

- Provider Key 只存在于 `.env`（0600）与 New API；teemoHub 只持有 New API Token。
- 数据库不保存 prompt/completion 正文，也不保存 API Key 明文（仅前缀/后缀 + 哈希）。
- 默认只监听 `127.0.0.1`；`runtime/`、`.env`、`apikey` 均被 `.gitignore` 忽略。
