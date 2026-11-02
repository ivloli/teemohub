import type { ReactNode } from "react";
import { Icon } from "./Icon";
import { Spinner } from "./Spinner";

type Props = {
  loading?: boolean;
  error?: string;
  empty?: boolean;
  emptyText?: string;
  loadingText?: string;
  onRetry?: () => void;
  children?: ReactNode;
};

export function PageState({
  loading,
  error,
  empty,
  emptyText = "暂无数据。",
  loadingText,
  onRetry,
  children,
}: Props) {
  if (loading) return <Spinner label={loadingText} />;
  if (error) {
    return (
      <div className="alert error state-error">
        <span>{error}</span>
        {onRetry && (
          <button className="btn ghost small-btn" type="button" onClick={onRetry}>
            <Icon name="refresh" size={14} /> 重试
          </button>
        )}
      </div>
    );
  }
  if (empty) return <div className="empty">{emptyText}</div>;
  return <>{children}</>;
}
