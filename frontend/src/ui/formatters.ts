export function formatTokenAmount(value: number) {
  const safeValue = Number.isFinite(value) ? value : 0;
  const absolute = Math.abs(safeValue);
  const compact = (scaled: number, suffix: string) => (
    `${Number(scaled.toFixed(Math.abs(scaled) >= 100 ? 0 : 1))} ${suffix}`
  );
  if (absolute >= 1_000_000_000) return compact(safeValue / 1_000_000_000, "B");
  if (absolute >= 1_000_000) return compact(safeValue / 1_000_000, "M");
  if (absolute >= 1_000) return compact(safeValue / 1_000, "K");
  return String(Math.round(safeValue));
}

export function formatTokens(value: number) {
  const amount = formatTokenAmount(value);
  return Math.abs(Number.isFinite(value) ? value : 0) >= 1_000 ? amount : `${amount} Token`;
}
