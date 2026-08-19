package.path = "gateway/?.lua;" .. package.path
package.preload["cjson.safe"] = function()
    return {}
end
package.preload["resty.sha256"] = function()
    return {
        new = function()
            return {
                update = function(self, value)
                    self.value = value
                    return true
                end,
                final = function(self)
                    return self.value
                end,
            }
        end,
    }
end
package.preload["resty.string"] = function()
    return {
        to_hex = function(value)
            return "hex:" .. value
        end,
    }
end

local request_gate = require "request_gate"

assert(request_gate.sha256_hex("external-key") == "hex:external-key")

local quota = request_gate.weekly_quota_payload(
    { raw_used_tokens = 120 },
    240,
    1000,
    2000
)
assert(quota.used_tokens == 240)
assert(quota.weighted_used_tokens == 240)
assert(quota.raw_used_tokens == 120)
assert(quota.limit_tokens == 1000)
assert(quota.week_end_at == 2000)
assert(quota.quota_unit == "weighted_tokens")

local legacy = request_gate.weekly_quota_payload({}, 240, 1000, 2000)
assert(legacy.raw_used_tokens == 240)
