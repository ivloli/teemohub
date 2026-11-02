const statusMap: Record<string, [string, string]> = {
  completed: ["已完成", "ok"],
  drained: ["已断流结算", "warn"],
  client_aborted: ["客户端中断", "danger"],
  upstream_error: ["上游错误", "danger"],
  reserved: ["已预留", "muted"],
  streaming: ["流式中", "info"],
};

export function StatusBadge({ status }: { status: string }) {
  const [label, tone] = statusMap[status] || [status || "-", "muted"];
  return <span className={`badge ${tone}`}>{label}</span>;
}

export function TrustBadge({ source }: { source: string }) {
  const map: Record<string, [string, string]> = {
    provider_final: ["provider_final", "ok"],
    provider_partial: ["provider_partial", "warn"],
    counted_text: ["counted_text", "violet"],
    estimate_text: ["estimate_text", "violet"],
    estimate_prompt_only: ["prompt_only", "violet"],
    none: ["none", "muted"],
  };
  const [label, tone] = map[source] || [source || "none", "muted"];
  return <span className={`pill ${tone}`}>{label}</span>;
}
