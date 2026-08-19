"use strict";

const assert = require("node:assert/strict");
const monitorUtils = require("../admin/static/monitor-utils.js");

assert.equal(
  monitorUtils.matchesSearchQuery("alice@example.com", "alice"),
  true
);
assert.equal(
  monitorUtils.matchesSearchQuery("bob@example.com", "alice"),
  false
);
assert.equal(
  monitorUtils.matchesSearchQuery("ALICE@example.com", "alice"),
  true
);
assert.equal(
  monitorUtils.matchesSearchQuery("alice@example.com", ""),
  true
);
assert.equal(
  monitorUtils.runtimeUnavailableDueToQuota({
    status_message: JSON.stringify({ error: { type: "usage_limit_reached" } })
  }),
  true
);
assert.equal(
  monitorUtils.runtimeUnavailableDueToQuota({
    status_message: JSON.stringify({ error: { code: "refresh_token_invalidated" } })
  }),
  false
);
assert.equal(
  monitorUtils.runtimeUnavailableDueToQuota({ status_message: "unauthorized" }),
  false
);

const series = [
  { name: "low-total", values: [200, 1], total: 201 },
  { name: "highest", values: [200, 9], total: 209 },
  { name: "middle", values: [200, 5], total: 205 },
  { name: "zero", values: [200, 0], total: 200 }
];

assert.deepEqual(
  monitorUtils.sortTooltipSeries(series, 1).map((item) => item.name),
  ["highest", "middle", "low-total", "zero"]
);
assert.deepEqual(
  monitorUtils.sortTooltipSeries(series, 0).map((item) => item.name),
  ["highest", "middle", "low-total", "zero"]
);

assert.deepEqual(
  monitorUtils.summarizeSeries([
    { name: "cpa-01", values: [10, 0, 30] },
    { name: "cpa-02", values: [5, 15, 20] }
  ], 3, "全部账号合计"),
  {
    name: "全部账号合计",
    values: [15, 15, 50],
    current: 50,
    average: 27,
    maximum: 50,
    total: 80
  }
);
assert.deepEqual(
  monitorUtils.summarizeSeries([], 0, "全部账号合计"),
  {
    name: "全部账号合计",
    values: [],
    current: 0,
    average: 0,
    maximum: 0,
    total: 0
  }
);

assert.deepEqual(monitorUtils.adaptivePointIndexes(0, 1000), []);
assert.deepEqual(monitorUtils.adaptivePointIndexes(1, 1000), [0]);
assert.deepEqual(monitorUtils.adaptivePointIndexes(6, 100, 20), [0, 1, 2, 3, 4, 5]);
assert.deepEqual(monitorUtils.adaptivePointIndexes(11, 100, 20), [0, 2, 4, 6, 8, 10]);
assert.deepEqual(monitorUtils.adaptivePointIndexes(10, 0, 10), [0, 9]);
const longRangeIndexes = monitorUtils.adaptivePointIndexes(169, 835, 10);
assert.equal(longRangeIndexes[0], 0);
assert.equal(longRangeIndexes.at(-1), 168);
assert.equal(longRangeIndexes.length, 84);

assert.deepEqual(
  monitorUtils.placeTooltip(
    { x: 100, y: 80 },
    { width: 60, height: 30 },
    { width: 200, height: 160 }
  ),
  { x: 114, y: 94, placement: "bottom-right", score: 0 }
);
assert.equal(
  monitorUtils.placeTooltip(
    { x: 180, y: 140 },
    { width: 60, height: 30 },
    { width: 200, height: 160 }
  ).placement,
  "top-left"
);
assert.equal(
  monitorUtils.placeTooltip(
    { x: 100, y: 80 },
    { width: 60, height: 30 },
    { width: 200, height: 160 },
    [{ x: 125, y: 105, radius: 8 }]
  ).placement,
  "top-right"
);

const groupedAccountUsage = monitorUtils.groupAccountModelUsage([
  {
    model: "gpt-5.6-sol",
    reasoning_effort: "xhigh",
    request_count: 3,
    input_tokens: 500,
    output_tokens: 80,
    reasoning_tokens: 50,
    cached_tokens: 200,
    total_tokens: 600,
    weighted_tokens: 1200
  },
  {
    model: "gpt-5.6-sol",
    reasoning_effort: "high",
    request_count: 2,
    input_tokens: 250,
    output_tokens: 50,
    reasoning_tokens: 30,
    cached_tokens: 100,
    total_tokens: 300
  },
  {
    model: "gpt-5.6-sol",
    reasoning_effort: "xhigh",
    request_count: 1,
    input_tokens: 80,
    output_tokens: 20,
    reasoning_tokens: 10,
    cached_tokens: 40,
    total_tokens: 100,
    weighted_tokens: 200
  },
  {
    model: "gpt-5.6-terra",
    reasoning_effort: "medium",
    request_count: 1,
    input_tokens: 160,
    output_tokens: 40,
    reasoning_tokens: 15,
    cached_tokens: 20,
    total_tokens: 200
  },
  {
    model: "gpt-5.6-terra",
    reasoning_effort: "low",
    request_count: 1,
    total_tokens: 0
  }
]);

assert.deepEqual(
  groupedAccountUsage.map((item) => [item.model, item.total_tokens]),
  [["gpt-5.6-sol", 1000], ["gpt-5.6-terra", 200]]
);
assert.deepEqual(
  groupedAccountUsage[0].efforts.map((item) => [
    item.reasoning_effort,
    item.request_count,
    item.total_tokens,
    item.weighted_tokens,
    item.share_percent
  ]),
  [["xhigh", 4, 700, 1400, 70], ["high", 2, 300, 0, 30]]
);
assert.deepEqual(
  groupedAccountUsage[1].efforts.map((item) => [
    item.reasoning_effort,
    item.total_tokens,
    item.share_percent
  ]),
  [["medium", 200, 100]]
);
groupedAccountUsage.forEach((model) => {
  assert.equal(
    model.efforts.reduce((total, effort) => total + effort.share_percent, 0),
    100
  );
});
assert.deepEqual(
  monitorUtils.groupAccountModelUsage([
    { model: "unknown", reasoning_effort: "", request_count: 1, total_tokens: 12 }
  ])[0],
  {
    model: "未上报模型",
    total_tokens: 12,
    efforts: [{
      reasoning_effort: "未上报强度",
      request_count: 1,
      success_count: 0,
      failed_count: 0,
      input_tokens: 0,
      output_tokens: 0,
      reasoning_tokens: 0,
      cached_tokens: 0,
      total_tokens: 12,
      weighted_tokens: 0,
      share_percent: 100
    }]
  }
);

assert.equal(monitorUtils.accountModelEffortColorKey("xhigh"), "xhigh");
assert.equal(monitorUtils.accountModelEffortColorKey("HIGH"), "high");
assert.equal(monitorUtils.accountModelEffortColorKey("未上报强度"), "unknown");

console.log("admin monitor interaction tests passed");
