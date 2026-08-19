(function registerTokenUsageFormatter(root, factory) {
  "use strict";

  const formatter = factory();
  if (typeof module === "object" && module.exports) module.exports = formatter;
  if (root) root.TokenUsageFormatter = formatter;
})(typeof globalThis === "undefined" ? this : globalThis, () => {
  "use strict";

  const amountFormatter = new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 });
  const exactFormatter = new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 });

  const normalize = (value) => {
    const numeric = Number(value);
    return Number.isFinite(numeric) && numeric >= 0 ? Math.floor(numeric) : 0;
  };

  const format = (input) => {
    const value = normalize(input);
    let divisor = 1;
    let unit = "Token";
    if (value >= 1_000_000_000) [divisor, unit] = [1_000_000_000, "B"];
    else if (value >= 1_000_000) [divisor, unit] = [1_000_000, "M"];
    else if (value >= 1_000) [divisor, unit] = [1_000, "K"];

    let rounded = Math.round((value / divisor) * 10) / 10;
    if (unit === "K" && rounded >= 1000) {
      [divisor, unit] = [1_000_000, "M"];
      rounded = Math.round((value / divisor) * 10) / 10;
    }
    if (unit === "M" && rounded >= 1000) {
      [divisor, unit] = [1_000_000_000, "B"];
      rounded = Math.round((value / divisor) * 10) / 10;
    }

    return {
      amount: amountFormatter.format(rounded),
      unit,
      label: `${exactFormatter.format(value)} Token`,
      compacted: divisor > 1
    };
  };

  const render = (value) => {
    const token = format(value);
    const exact = token.compacted
      ? `<small class="token-usage-exact" aria-hidden="true">${token.label}</small>`
      : "";
    return `<span class="token-usage"><span class="token-usage-main" aria-hidden="true"><span class="token-usage-value">${token.amount}</span><small class="token-usage-unit">${token.unit}</small></span>${exact}<span class="token-usage-sr-only">${token.label}</span></span>`;
  };

  return { format, render };
});
