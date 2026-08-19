local cjson = require "cjson.safe"
local resty_sha256 = require "resty.sha256"
local resty_string = require "resty.string"

local _M = {}
local AUTH_LOADER_MAX_AGE_SECONDS = 5

local function sha256_hex(value)
    local digest = resty_sha256:new()
    if not digest or not digest:update(value) then
        return nil
    end
    return resty_string.to_hex(digest:final())
end

_M.sha256_hex = sha256_hex

local function weekly_quota_payload(user_quota, used, limit, week_end)
    return {
        used_tokens = used,
        weighted_used_tokens = used,
        raw_used_tokens = tonumber(user_quota.raw_used_tokens) or used,
        limit_tokens = limit,
        week_end_at = week_end,
        quota_unit = "weighted_tokens",
    }
end

_M.weekly_quota_payload = weekly_quota_payload

local function json_exit(status, payload, headers)
    ngx.status = status
    ngx.header["Content-Type"] = "application/json; charset=utf-8"
    ngx.header["Cache-Control"] = "no-store"
    for name, value in pairs(headers or {}) do
        ngx.header[name] = value
    end
    ngx.say(cjson.encode(payload))
    return ngx.exit(status)
end

local function warn_fail_open(reason)
    local dict = ngx.shared.quota_cache
    if dict:add("warning:" .. reason, true, 60) then
        ngx.log(ngx.ERR, "user quota protection unavailable; request allowed: ", reason)
    end
end

function _M.authorize(options)
    options = options or {}
    local authorization = ngx.var.http_authorization or ""
    local external_key = authorization:match("^[Bb][Ee][Aa][Rr][Ee][Rr]%s+([^%s]+)%s*$")
    local auth = ngx.shared.auth_cache
    local now = ngx.time()
    local generation = auth:get("active_generation")
    local auth_loader_success_at = tonumber(auth:get("snapshot_loader_success_at")) or 0
    if not generation or generation == "" or auth_loader_success_at <= 0
        or now - auth_loader_success_at > AUTH_LOADER_MAX_AGE_SECONDS then
        return json_exit(503, {
            error = {
                message = "API authentication is temporarily unavailable",
                type = "server_error",
                code = "authentication_snapshot_unavailable",
            },
        }, { ["Retry-After"] = "1" })
    end
    if not external_key then
        return json_exit(401, {
            error = { message = "Invalid API key", type = "invalid_request_error" },
        })
    end
    local digest = sha256_hex(external_key)
    if not digest then
        return json_exit(503, {
            error = {
                message = "API authentication is temporarily unavailable",
                type = "server_error",
                code = "authentication_hash_unavailable",
            },
        }, { ["Retry-After"] = "1" })
    end
    local raw = auth:get("g:" .. generation .. ":" .. digest)
    local identity = raw and cjson.decode(raw) or nil
    if type(identity) ~= "table" then
        return json_exit(401, {
            error = { message = "Invalid API key", type = "invalid_request_error" },
        })
    end

    ngx.var.key_label = identity.label
    ngx.var.account_name = identity.account
    ngx.var.backend = identity.backend
    ngx.var.upstream_authorization = "Bearer " .. identity.internal_key

    if options.enforce_quota == false then
        return
    end
    local quota = ngx.shared.quota_cache
    local last_success_at = tonumber(quota:get("last_success_at")) or 0
    local loader_success_at = tonumber(quota:get("snapshot_loader_success_at")) or 0
    local fail_open_after = tonumber(quota:get("fail_open_after")) or 300
    if last_success_at <= 0 or now - last_success_at > fail_open_after then
        warn_fail_open("collector_last_success")
        return
    end
    if loader_success_at <= 0 or now - loader_success_at > fail_open_after then
        warn_fail_open("snapshot_loader")
        return
    end
    local quota_generation = quota:get("active_generation")
    if not quota_generation or quota_generation == "" then
        warn_fail_open("snapshot_missing")
        return
    end
    local quota_raw = quota:get("g:" .. quota_generation .. ":" .. identity.user_email)
    local user_quota = quota_raw and cjson.decode(quota_raw) or nil
    if type(user_quota) ~= "table" then
        warn_fail_open("user_record_missing")
        return
    end
    local limit = tonumber(user_quota.limit_tokens) or -1
    local used = tonumber(user_quota.used_tokens) or 0
    local week_end = tonumber(user_quota.week_end_at) or now
    if now >= week_end then
        warn_fail_open("snapshot_period_expired")
        return
    end
    if limit >= 0 and used >= limit then
        local retry_after = math.max(1, math.floor(week_end - now))
        return json_exit(429, {
            error = {
                message = "Weekly user token quota exceeded",
                type = "tokens",
                code = "weekly_user_token_quota_exceeded",
            },
            user_weekly_quota = weekly_quota_payload(
                user_quota,
                used,
                limit,
                week_end
            ),
        }, { ["Retry-After"] = tostring(retry_after) })
    end
end

return _M
