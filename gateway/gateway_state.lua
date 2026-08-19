local cjson = require "cjson.safe"

local _M = {}

local AUTH_FILE = os.getenv("CLIPROXY_AUTH_SNAPSHOT_FILE")
    or "/var/run/cliproxy-snapshots/auth-snapshot.json"
local QUOTA_FILE = os.getenv("CLIPROXY_QUOTA_SNAPSHOT_FILE")
    or "/var/run/cliproxy-snapshots/quota-snapshot.json"
local HEARTBEAT_FILE = os.getenv("CLIPROXY_QUOTA_HEARTBEAT_FILE")
    or "/var/run/cliproxy-snapshots/quota-heartbeat.json"
local MAX_SNAPSHOT_BYTES = 16 * 1024 * 1024

local function read_json(path)
    local handle, open_error = io.open(path, "rb")
    if not handle then
        return nil, open_error or "open failed"
    end
    local raw = handle:read(MAX_SNAPSHOT_BYTES + 1)
    handle:close()
    if not raw or #raw == 0 then
        return nil, "empty snapshot"
    end
    if #raw > MAX_SNAPSHOT_BYTES then
        return nil, "snapshot too large"
    end
    local payload, decode_error = cjson.decode(raw)
    if type(payload) ~= "table" then
        return nil, decode_error or "snapshot must be an object"
    end
    return payload
end

local function valid_hex(value, length)
    -- Lua patterns do not support regex repetition such as ``{32}``.
    return type(value) == "string"
        and #value == length
        and value:match("^[0-9a-f]+$") ~= nil
end

local function valid_generation(value)
    return valid_hex(value, 32)
end

_M.valid_hex = valid_hex
_M.valid_generation = valid_generation

local function quota_record_payload(record)
    local used_tokens = math.floor(record.used_tokens)
    return {
        week_start_at = math.floor(record.week_start_at),
        week_end_at = math.floor(record.week_end_at),
        limit_tokens = math.floor(record.limit_tokens),
        used_tokens = used_tokens,
        raw_used_tokens = math.floor(tonumber(record.raw_used_tokens) or used_tokens),
        weighted_raw_used_tokens = math.floor(
            tonumber(record.weighted_raw_used_tokens) or used_tokens
        ),
        quota_unit = "weighted_tokens",
    }
end

_M.quota_record_payload = quota_record_payload

local function cleanup_generation(dict, generation)
    if not generation or generation == "" then
        return
    end
    local prefix = "g:" .. generation .. ":"
    for _, key in ipairs(dict:get_keys(0)) do
        if key:sub(1, #prefix) == prefix then
            dict:delete(key)
        end
    end
end

local function cleanup_obsolete(dict, active, previous)
    local generations = {}
    for _, key in ipairs(dict:get_keys(0)) do
        local generation = key:match("^g:([0-9a-f]+):")
        if generation and generation ~= active and generation ~= previous then
            generations[generation] = true
        end
    end
    for generation, _ in pairs(generations) do
        cleanup_generation(dict, generation)
    end
end

local function load_auth()
    local payload, read_error = read_json(AUTH_FILE)
    if not payload then
        return nil, read_error
    end
    if payload.version ~= 1 or not valid_generation(payload.generation)
        or type(payload.records) ~= "table" then
        return nil, "invalid auth snapshot envelope"
    end
    local dict = ngx.shared.auth_cache
    if dict:get("active_generation") == payload.generation then
        dict:set("snapshot_loader_success_at", ngx.time())
        return true
    end
    local generation = payload.generation
    cleanup_generation(dict, generation)
    local seen = {}
    for _, record in ipairs(payload.records) do
        local digest = type(record) == "table" and record.external_key_sha256 or nil
        local valid = valid_hex(digest, 64)
            and type(record.user_email) == "string" and record.user_email ~= ""
            and type(record.account) == "string" and record.account:match("^[a-z][a-z0-9-]+$")
            and type(record.backend) == "string" and record.backend:match("^cliproxy%-[a-z][a-z0-9-]+:8317$")
            and type(record.internal_key) == "string" and record.internal_key ~= ""
            and type(record.label) == "string" and record.label ~= ""
            and not seen[digest]
        if not valid then
            cleanup_generation(dict, generation)
            return nil, "invalid auth snapshot record"
        end
        seen[digest] = true
        local encoded = cjson.encode({
            user_email = record.user_email,
            account = record.account,
            backend = record.backend,
            internal_key = record.internal_key,
            label = record.label,
        })
        local ok, set_error = dict:set("g:" .. generation .. ":" .. digest, encoded)
        if not ok then
            cleanup_generation(dict, generation)
            return nil, set_error or "auth shared dict is full"
        end
    end
    local previous = dict:get("active_generation")
    dict:set("previous_generation", previous or "")
    dict:set("generated_at:" .. generation, tonumber(payload.generated_at) or 0)
    dict:set("record_count:" .. generation, #payload.records)
    dict:set("active_generation", generation)
    dict:set("snapshot_loader_success_at", ngx.time())
    cleanup_obsolete(dict, generation, previous)
    return true
end

local function load_quota()
    local payload, read_error = read_json(QUOTA_FILE)
    if not payload then
        return nil, read_error
    end
    if payload.version ~= 1 or not valid_generation(payload.generation)
        or type(payload.records) ~= "table" then
        return nil, "invalid quota snapshot envelope"
    end
    local dict = ngx.shared.quota_cache
    if dict:get("active_generation") == payload.generation then
        dict:set("snapshot_loader_success_at", ngx.time())
        return true
    end
    local generation = payload.generation
    cleanup_generation(dict, generation)
    local seen = {}
    for _, record in ipairs(payload.records) do
        local user = type(record) == "table" and record.user_email or nil
        local valid = type(user) == "string" and user ~= "" and not seen[user]
            and type(record.week_start_at) == "number"
            and type(record.week_end_at) == "number"
            and record.week_end_at > record.week_start_at
            and type(record.limit_tokens) == "number" and record.limit_tokens >= -1
            and type(record.used_tokens) == "number" and record.used_tokens >= 0
        if not valid then
            cleanup_generation(dict, generation)
            return nil, "invalid quota snapshot record"
        end
        seen[user] = true
        local encoded = cjson.encode(quota_record_payload(record))
        local ok, set_error = dict:set("g:" .. generation .. ":" .. user, encoded)
        if not ok then
            cleanup_generation(dict, generation)
            return nil, set_error or "quota shared dict is full"
        end
    end
    local previous = dict:get("active_generation")
    dict:set("previous_generation", previous or "")
    dict:set("generated_at:" .. generation, tonumber(payload.generated_at) or 0)
    dict:set("record_count:" .. generation, #payload.records)
    dict:set("active_generation", generation)
    dict:set("snapshot_loader_success_at", ngx.time())
    cleanup_obsolete(dict, generation, previous)
    return true
end

local function load_heartbeat()
    local payload, read_error = read_json(HEARTBEAT_FILE)
    if not payload then
        return nil, read_error
    end
    if payload.version ~= 1 or type(payload.updated_at) ~= "number"
        or type(payload.ok) ~= "boolean"
        or type(payload.last_success_at) ~= "number"
        or type(payload.fail_open_after_seconds) ~= "number" then
        return nil, "invalid quota heartbeat"
    end
    local dict = ngx.shared.quota_cache
    dict:set("heartbeat_at", math.floor(payload.updated_at))
    dict:set("heartbeat_ok", payload.ok and 1 or 0)
    dict:set("heartbeat_stale_after", math.max(5, math.floor(tonumber(payload.stale_after_seconds) or 15)))
    dict:set("last_success_at", math.max(0, math.floor(payload.last_success_at)))
    dict:set("fail_open_after", math.max(30, math.floor(payload.fail_open_after_seconds)))
    return true
end

local function refresh(premature)
    if premature then
        return
    end
    local ok, error_message = load_auth()
    if not ok then
        ngx.log(ngx.ERR, "auth snapshot refresh failed: ", error_message or "unknown")
    end
    local quota_ok, quota_error = load_quota()
    if not quota_ok then
        ngx.log(ngx.ERR, "quota snapshot refresh failed (fail-open): ", quota_error or "unknown")
    end
    local heartbeat_ok, heartbeat_error = load_heartbeat()
    if not heartbeat_ok then
        ngx.log(ngx.ERR, "quota heartbeat refresh failed (fail-open): ", heartbeat_error or "unknown")
    end
end

function _M.start()
    if ngx.worker.id() ~= 0 then
        return
    end
    local ok, timer_error = ngx.timer.every(0.5, refresh)
    if not ok then
        ngx.log(ngx.ERR, "failed to start gateway snapshot loader: ", timer_error)
        return
    end
    local immediate_ok, immediate_error = ngx.timer.at(0, refresh)
    if not immediate_ok then
        ngx.log(ngx.ERR, "failed to schedule initial gateway snapshot load: ", immediate_error)
    end
end

local function kind_status(dict)
    local generation = dict:get("active_generation") or ""
    return {
        active_generation = generation,
        previous_generation = dict:get("previous_generation") or "",
        generated_at = generation ~= "" and (dict:get("generated_at:" .. generation) or 0) or 0,
        record_count = generation ~= "" and (dict:get("record_count:" .. generation) or 0) or 0,
    }
end

function _M.status()
    local result = {
        auth = kind_status(ngx.shared.auth_cache),
        quota = kind_status(ngx.shared.quota_cache),
    }
    result.auth.snapshot_loader_success_at = ngx.shared.auth_cache:get("snapshot_loader_success_at") or 0
    result.quota.heartbeat_at = ngx.shared.quota_cache:get("heartbeat_at") or 0
    result.quota.heartbeat_ok = ngx.shared.quota_cache:get("heartbeat_ok") == 1
    result.quota.heartbeat_stale_after = ngx.shared.quota_cache:get("heartbeat_stale_after") or 0
    result.quota.last_success_at = ngx.shared.quota_cache:get("last_success_at") or 0
    result.quota.fail_open_after = ngx.shared.quota_cache:get("fail_open_after") or 0
    result.quota.snapshot_loader_success_at = ngx.shared.quota_cache:get("snapshot_loader_success_at") or 0
    return result
end

return _M
