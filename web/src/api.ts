export type User = {
  id: number;
  username: string;
  role: "admin" | "user";
  display_name: string;
  balance_nano: number;
  balance_cny: string;
  created_at: number;
};

export type APIKey = {
  id: number;
  user_id: number;
  name: string;
  prefix: string;
  suffix: string;
  status: number;
  rpm: number;
  tpm: number;
  concurrent: number;
  max_output: number;
  created_at: number;
  last_used_at: number;
};

export type PublicModel = {
  id: string;
  display_name: string;
  family: string;
  input_price_nano: number;
  output_price_nano: number;
  cached_input_price_nano: number;
  context_window: number;
  max_output: number;
  enabled: boolean;
  updated_at: number;
  price_version_id: number;
};

export type Model = PublicModel & {
  upstream_name: string;
  provider_input_price_nano: number;
  provider_output_price_nano: number;
  provider_cached_input_price_nano: number;
};

export type PriceTick = {
  input_price_nano: number;
  output_price_nano: number;
  provider_input_price_nano: number;
  provider_output_price_nano: number;
  cached_input_price_nano: number;
  provider_cached_input_price_nano: number;
};

export type ModelPriceVersion = {
  id: number;
  model_id: string;
  price: PriceTick;
  actor: string;
  note: string;
  created_at: number;
};

export type AuditEvent = {
  id: number;
  actor_id: number;
  actor_name: string;
  actor_type: string;
  action: string;
  resource: string;
  resource_id: string;
  before_json: string;
  after_json: string;
  reason: string;
  created_at: number;
};

export type UsageEvent = {
  id: number;
  request_id: string;
  attempt: number;
  final: boolean;
  source: string;
  trust: string;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cached_input_tokens: number;
  snapshot_seq: number;
  local_tokens_at_snapshot: number;
  local_output_tokens: number;
  detail_json: string;
  created_at: number;
};

export type LedgerEntry = {
  id: number;
  request_id: string;
  user_id: number;
  entry_type: string;
  amount_nano: number;
  balance_after_nano: number;
  memo: string;
  created_at: number;
};

export type RequestRow = {
  id: string;
  user_id: number;
  api_key_id: number;
  model: string;
  endpoint: string;
  stream: boolean;
  status: string;
  http_status: number;
  client_disconnected: boolean;
  upstream_finished: boolean;
  usage_source: string;
  usage_trust: string;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cached_input_tokens: number;
  cost_nano: number;
  provider_cost_nano: number;
  reserved_nano: number;
  price_version: number;
  price_version_id: number;
  latency_ms: number;
  first_token_ms: number;
  created_at: number;
  finished_at: number;
  error_message: string;
  trace_id: string;
};

export type DailyUsage = {
  date: string;
  requests: number;
  cost_nano: number;
  input_tokens: number;
  output_tokens: number;
};

export type Summary = {
  today_cost_nano: number;
  today_requests: number;
  today_input_tokens: number;
  today_output_tokens: number;
  total_cost_nano: number;
  total_requests: number;
  daily: DailyUsage[];
};

export type AnalyticsTotals = {
  requests: number;
  cost_nano: number;
  provider_cost_nano: number;
  input_tokens: number;
  cached_input_tokens: number;
  output_tokens: number;
  failed: number;
  disconnected: number;
  avg_latency_ms: number;
  avg_first_token_ms: number;
};

export type AnalyticsDaily = {
  date: string;
  requests: number;
  cost_nano: number;
  provider_cost_nano: number;
  input_tokens: number;
  cached_input_tokens: number;
  output_tokens: number;
  failed: number;
};

export type ModelUsage = {
  model: string;
  requests: number;
  cost_nano: number;
  provider_cost_nano: number;
  input_tokens: number;
  cached_input_tokens: number;
  output_tokens: number;
  avg_latency_ms: number;
};

export type StatusUsage = { status: string; requests: number; cost_nano: number };

export type KeyUsage = {
  api_key_id: number;
  name: string;
  prefix: string;
  suffix: string;
  requests: number;
  cost_nano: number;
};

export type Analytics = {
  days: number;
  since: number;
  totals: AnalyticsTotals;
  daily: AnalyticsDaily[];
  models: ModelUsage[];
  statuses: StatusUsage[];
  keys: KeyUsage[];
};

export type HealthCheck = {
  name: string;
  ok: boolean;
  error?: string;
  latency_ms: number;
  detail?: string;
};

export type Health = {
  ok: boolean;
  upstream: string;
  error: string;
  checks?: HealthCheck[];
};

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    credentials: "include",
    headers: init?.body ? { "Content-Type": "application/json", ...(init?.headers || {}) } : init?.headers,
    ...init,
  });
  const text = await res.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = text;
  }
  if (!res.ok) {
    const msg =
      (data as any)?.error?.message ||
      (typeof data === "string" ? data : "") ||
      `HTTP ${res.status}`;
    const err = new Error(msg) as Error & { status?: number };
    err.status = res.status;
    throw err;
  }
  return data as T;
}

export function fmtCNY(nano: number): string {
  const cny = nano / 1e8;
  return `${cny < 0 ? "-" : ""}¥${Math.abs(cny).toFixed(6)}`;
}

export function fmtCNYShort(nano: number): string {
  const cny = nano / 1e8;
  const sign = cny < 0 ? "-" : "";
  const abs = Math.abs(cny);
  if (abs === 0) return "¥0";
  if (abs < 0.01) return `${sign}¥${abs.toFixed(4)}`;
  return `${sign}¥${abs.toFixed(2)}`;
}

export function fmtCNYPerM(nanoPerToken: number): string {
  return `¥${(nanoPerToken / 100).toFixed(2)}/M`;
}

export function fmtNum(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(2)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

export function fmtPct(part: number, whole: number): string {
  if (!whole) return "—";
  return `${((part / whole) * 100).toFixed(1)}%`;
}

export function fmtTime(ms: number): string {
  if (!ms) return "-";
  const d = new Date(ms);
  return d.toLocaleString("zh-CN", { hour12: false });
}

export function fmtClock(ms: number): string {
  if (!ms) return "-";
  return new Date(ms).toLocaleTimeString("zh-CN", { hour12: false });
}
