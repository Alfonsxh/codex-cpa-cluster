"use strict";

const assert = require("node:assert/strict");
const viewState = require("../admin/static/view-state-utils.js");

const catalog = {
  accounts: [
    {
      id: "cpa-main",
      operational_status: {
        code: "available",
        label: "可用",
        tone: "success",
        selectable: true
      }
    },
    {
      id: "cpa-paused",
      operational_status: {
        code: "disabled",
        label: "已停用",
        tone: "neutral",
        selectable: false
      }
    }
  ],
  users: [
    { email: "active@example.com", status: "active" },
    { email: "inactive@example.com", status: "inactive" }
  ]
};

assert.deepEqual(viewState.catalogOptions(catalog, "account"), [
  { value: "cpa-main", label: "cpa-main" },
  { value: "cpa-paused", label: "cpa-paused" }
]);
assert.deepEqual(viewState.catalogOptions(catalog, "user"), [
  { value: "active@example.com", label: "active@example.com" },
  { value: "inactive@example.com", label: "inactive@example.com" }
]);
assert.deepEqual(viewState.catalogOptions(null, "account"), []);

assert.deepEqual(
  viewState.monitorSeriesStatus(catalog, "cpa-main", "account"),
  { label: "可用", tone: "success" }
);
assert.deepEqual(
  viewState.monitorSeriesStatus(catalog, "active@example.com", "user"),
  { label: "活跃", tone: "success" }
);
assert.deepEqual(
  viewState.monitorSeriesStatus(catalog, "inactive@example.com", "user"),
  { label: "停用", tone: "neutral" }
);
assert.deepEqual(
  viewState.monitorSeriesStatus(null, "cpa-main", "account"),
  { label: "状态未知", tone: "neutral" }
);
assert.deepEqual(
  viewState.monitorSeriesStatus(catalog, "missing@example.com", "user"),
  { label: "状态未知", tone: "neutral" }
);

assert.deepEqual(
  viewState.allUserQuotaImpact({
    total_users: 237,
    users_with_usage: 184,
    total_used_tokens: 900,
    total_raw_used_tokens: 800
  }),
  {
    available: true,
    totalUsers: 237,
    usersWithUsage: 184,
    totalUsedTokens: 900,
    totalRawUsedTokens: 800
  }
);
assert.equal(viewState.allUserQuotaImpact({}).available, false);
assert.equal(viewState.allUserQuotaImpact({ total_users: null }).available, false);
assert.equal(viewState.allUserQuotaImpact({ total_users: "" }).available, false);

assert.deepEqual(
  viewState.mutationAffectedViews("/users/team", "PUT"),
  ["overview", "accounts", "users", "organization", "settings"]
);
assert.deepEqual(
  viewState.mutationAffectedViews("/accounts/policy", "POST"),
  ["overview", "accounts", "users", "operations"]
);
assert.deepEqual(
  viewState.mutationAffectedViews("/overview/catalog", "GET"),
  []
);

assert.equal(
  viewState.stopOperationMessage("cpa-main", {
    target_type: "account",
    routed_users: 12
  }),
  "将停止 cpa-main，当前有 12 个用户路由到该账号。"
);
assert.equal(
  viewState.stopOperationMessage("cpa-main", {
    target_type: "account",
    routed_users: 0
  }),
  "将停止 cpa-main，当前没有用户路由到该账号。"
);
assert.equal(
  viewState.stopOperationMessage("usage-collector", {
    target_type: "service",
    routed_users: null
  }),
  "将停止 usage-collector。"
);
assert.equal(
  viewState.stopOperationMessage("cpa-main", null, false),
  "将停止 cpa-main；影响范围暂不可确认。"
);
assert.equal(
  viewState.stopOperationMessage("cpa-main", {
    target_type: "account",
    routed_users: null
  }),
  "将停止 cpa-main；影响范围暂不可确认。"
);

console.log("admin view state tests passed");
