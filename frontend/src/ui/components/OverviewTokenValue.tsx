import { formatTokens } from "../formatters";

export function OverviewTokenValue({ value }: { value: number }) {
  const tokens = Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0;
  return (
    <span className="overview-token-value">
      <strong>{formatTokens(tokens)}</strong>
      <small>{tokens.toLocaleString("en-US", { maximumFractionDigits: 0 })}</small>
    </span>
  );
}
