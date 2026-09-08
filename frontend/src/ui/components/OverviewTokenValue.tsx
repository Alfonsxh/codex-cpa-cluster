import { formatTokens } from "../formatters";

export function OverviewTokenValue({ value }: { value: number }) {
  const tokens = Number.isFinite(value) ? Math.max(0, Math.floor(value)) : 0;
  const [amount, unit = "Token"] = formatTokens(tokens).split(" ");
  return (
    <span className="overview-token-value">
      <strong>{amount} <em>{unit}</em></strong>
      <small>{tokens.toLocaleString("en-US", { maximumFractionDigits: 0 })} Token</small>
    </span>
  );
}
