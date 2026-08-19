"use strict";

const assert = require("node:assert/strict");
const formatter = require("../portal/token-usage.js");

const expected = [
  [0, "0", "Token", "0 Token", false],
  [999, "999", "Token", "999 Token", false],
  [1000, "1", "K", "1,000 Token", true],
  [12345, "12.3", "K", "12,345 Token", true],
  [999950, "1", "M", "999,950 Token", true],
  [1000000, "1", "M", "1,000,000 Token", true],
  [999950000, "1", "B", "999,950,000 Token", true],
  [1234567890, "1.2", "B", "1,234,567,890 Token", true]
];

for (const [input, amount, unit, label, compacted] of expected) {
  assert.deepEqual(formatter.format(input), { amount, unit, label, compacted });
}

for (const input of [undefined, null, "not-a-number", -1, Number.POSITIVE_INFINITY]) {
  assert.deepEqual(formatter.format(input), {
    amount: "0", unit: "Token", label: "0 Token", compacted: false
  });
}

const compactHtml = formatter.render(12345);
assert.match(compactHtml, /12\.3/);
assert.match(compactHtml, />K</);
assert.match(compactHtml, /12,345 Token/);
assert.match(compactHtml, /token-usage-exact/);
assert.match(compactHtml, /token-usage-sr-only/);

const rawHtml = formatter.render(999);
assert.match(rawHtml, />999</);
assert.match(rawHtml, />Token</);
assert.doesNotMatch(rawHtml, /token-usage-exact/);

console.log("token usage formatter tests passed");
