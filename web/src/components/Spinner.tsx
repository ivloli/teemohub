export function Spinner({ label }: { label?: string }) {
  return (
    <div className="spinner-row">
      <span className="spinner" />
      {label && <span className="muted">{label}</span>}
    </div>
  );
}
