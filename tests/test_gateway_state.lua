package.path = "gateway/?.lua;" .. package.path
package.preload["cjson.safe"] = function()
    return {}
end

local gateway_state = require "gateway_state"

assert(gateway_state.valid_generation(string.rep("0", 32)))
assert(gateway_state.valid_generation("e16b304e9e764462b493857abe6200aa"))
assert(not gateway_state.valid_generation(string.rep("0", 31)))
assert(not gateway_state.valid_generation(string.rep("0", 33)))
assert(not gateway_state.valid_generation("e16b304e9e764462b493857abe6200ag"))
assert(not gateway_state.valid_generation(nil))

local weighted = gateway_state.quota_record_payload({
    week_start_at = 100.9,
    week_end_at = 200.9,
    limit_tokens = 1000.9,
    used_tokens = 240.9,
    raw_used_tokens = 120.9,
    weighted_raw_used_tokens = 260.9,
})
assert(weighted.week_start_at == 100)
assert(weighted.week_end_at == 200)
assert(weighted.limit_tokens == 1000)
assert(weighted.used_tokens == 240)
assert(weighted.raw_used_tokens == 120)
assert(weighted.weighted_raw_used_tokens == 260)
assert(weighted.quota_unit == "weighted_tokens")

local legacy = gateway_state.quota_record_payload({
    week_start_at = 100,
    week_end_at = 200,
    limit_tokens = 1000,
    used_tokens = 240,
})
assert(legacy.raw_used_tokens == 240)
assert(legacy.weighted_raw_used_tokens == 240)

assert(gateway_state.valid_hex(string.rep("a", 64), 64))
assert(not gateway_state.valid_hex(string.rep("a", 63), 64))
assert(not gateway_state.valid_hex(string.rep("a", 65), 64))
assert(not gateway_state.valid_hex(string.rep("a", 63) .. "z", 64))
