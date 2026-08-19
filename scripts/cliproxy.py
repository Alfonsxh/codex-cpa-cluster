#!/usr/bin/env python3
"""CLIProxyAPI 多账号部署的控制脚本。"""

import argparse
import base64
import contextlib
import errno
import fcntl
import hashlib
import ipaddress
import json
import math
import os
import re
import secrets
import signal
import shutil
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
DEFAULT_ROOT = Path(
    os.environ.get("CLIPROXY_ROOT", Path(__file__).resolve().parents[1])
).resolve()
if str(SCRIPT_DIR) not in sys.path:
    sys.path.insert(0, str(SCRIPT_DIR))
from branding import validate_logo
from control_plane_store import ControlPlaneStore, PROFILE_DIGEST_METADATA_KEY
from edge_slot import ensure_active_slot
try:
    from zoneinfo import ZoneInfo, ZoneInfoNotFoundError
except ImportError:  # Python 3.8 compatibility for local control commands.
    ZoneInfo = None

    class ZoneInfoNotFoundError(Exception):
        pass


USER_EMAIL_RE = re.compile(r"^[a-z0-9.!#$%&'*+/=?^_`{|}~-]+@([a-z0-9.-]+)$")
ACCOUNT_EMAIL_RE = re.compile(r"^[^\s@]+@[^\s@]+\.[^\s@]+$")
ACCOUNT_ID_RE = re.compile(r"^[a-z][a-z0-9-]{1,31}$")
RESERVED_ACCOUNT_IDS = {"admin", "all", "gateway", "management"}
ACCOUNT_PORT_START = 18319
ACCOUNT_PORT_END = 18999
DEFAULT_PROXY_SECRET = "cpa_default_proxy_url"
ACCOUNT_PROXY_SECRET_PREFIX = "cpa_account_proxy_url:"
UNSET = object()
INTERNAL_MODELS_PROBE_PATH = "/__internal/probe/models"
INFLIGHT_STATS_HTTP_TIMEOUT_SECONDS = 1
INFLIGHT_STATS_MAX_RESPONSE_BYTES = 1024 * 1024
CLIPROXY_IDENTITY_TIMEOUT_SECONDS = 15
CLIPROXY_IDENTITY_MAX_OUTPUT_BYTES = 64 * 1024
CLIPROXY_VERSION_BANNER_RE = re.compile(
    r"CLIProxyAPI Version:\s*([^,\s]+),\s*Commit:\s*([^,\s]+),\s*BuiltAt:\s*([^\r\n]+)"
)
SEMANTIC_VERSION_RE = re.compile(
    r"v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?"
)
CONFIG_VERSION = 1
INTERNAL_KEYS_VERSION = 1
GATEWAY_SNAPSHOT_VERSION = 1
GATEWAY_SNAPSHOT_GID = 65534
RUNTIME_OPERATION_LOCK_PATH = "state/runtime-operation.lock"
COMPOSE_ENV_PATH = "state/compose.env"
COMPOSE_ENV_LOCK_PATH = "state/compose-env.lock"
LEGACY_ENV_BACKUP_PATH = "state/legacy.env"
BOOTSTRAP_ENV_DEFAULTS = {
    "INSTANCE_NAME": "cliproxy",
    "COMPOSE_PROJECT_NAME": "cliproxy-multi",
    "DOCKER_NETWORK_NAME": "cliproxy-backend",
}
DEFAULT_ADMIN_BASE_IMAGE = (
    "docker.m.daocloud.io/library/docker:27.5.1-cli@"
    "sha256:851f91d241214e7c6db86513b270d58776379aacc5eb9c4a87e5b47115e3065c"
)
DEFAULT_GATEWAY_BASE_IMAGE = (
    "docker.m.daocloud.io/openresty/openresty:1.31.1.1-2-alpine-fat@"
    "sha256:427d94fea0c24b099e7891e8d1b7976f6d008e2d427e56bab725c8b8b293795b"
)
PINNED_DEFAULT_IMAGE_MIGRATIONS = {
    "runtime.admin_base_image": {
        "docker:27.5.1-cli": DEFAULT_ADMIN_BASE_IMAGE,
        "docker.m.daocloud.io/library/docker:27.5.1-cli": DEFAULT_ADMIN_BASE_IMAGE,
    },
    "runtime.gateway_image": {
        "openresty/openresty:1.31.1.1-2-alpine-fat": DEFAULT_GATEWAY_BASE_IMAGE,
        "openresty/openresty:alpine-fat": DEFAULT_GATEWAY_BASE_IMAGE,
        "docker.m.daocloud.io/openresty/"
        "openresty:1.31.1.1-2-alpine-fat": DEFAULT_GATEWAY_BASE_IMAGE,
    },
}
LEGACY_ENV_SETTING_KEYS = {
    "runtime.cliproxy_image": "CLIPROXY_IMAGE",
    "runtime.gateway_image": "GATEWAY_IMAGE",
    "runtime.admin_base_image": "ADMIN_BASE_IMAGE",
    "accounts.listen_address": "BUSINESS_CPA_LISTEN_ADDRESS",
    "management.listen_address": "MANAGEMENT_LISTEN_ADDRESS",
    "gateway.listen_address": "GATEWAY_LISTEN_ADDRESS",
    "gateway.port": "GATEWAY_PORT",
    "gateway.internal_port": "GATEWAY_INTERNAL_PORT",
    "management.port": "MANAGEMENT_PORT",
    "delivery.gateway_drain_timeout_seconds": "GATEWAY_DRAIN_TIMEOUT_SECONDS",
    "delivery.release_metadata_image": "RELEASE_METADATA_IMAGE",
}
CONFIG_ENV_KEYS = LEGACY_ENV_SETTING_KEYS
COMPOSE_SETTING_ENV_KEYS = {
    "accounts.listen_address": "BUSINESS_CPA_LISTEN_ADDRESS",
    "management.listen_address": "MANAGEMENT_LISTEN_ADDRESS",
    "management.port": "MANAGEMENT_PORT",
    "gateway.listen_address": "GATEWAY_LISTEN_ADDRESS",
    "gateway.port": "GATEWAY_PORT",
    "gateway.internal_port": "GATEWAY_INTERNAL_PORT",
}
LEGACY_DEPLOYMENT_ENV_KEYS = {
    "RELEASE_VERSION": "version",
    "ADMIN_IMAGE": "admin_image",
    "WEB_RUNTIME_IMAGE": "web_image",
    "GATEWAY_RUNTIME_IMAGE": "gateway_image",
    "EDGE_RUNTIME_IMAGE": "edge_image",
}
BOOTSTRAP_ENV_KEYS = (
    "DEPLOY_ROOT",
    "INSTANCE_NAME",
    "COMPOSE_PROJECT_NAME",
    "DOCKER_NETWORK_NAME",
)
RETIRED_CONFIG_KEYS = {
    "gost.enabled",
    "gost.remote_hosts",
    "gost.remote_host",
    "gost.port_start",
    "gost.port_end",
    "runtime.gost_image",
}
REASONING_EFFORT_MULTIPLIER_DEFAULTS = (
    ("none", "None", 1.0),
    ("minimal", "Minimal", 1.0),
    ("low", "Low", 1.0),
    ("medium", "Medium", 1.0),
    ("high", "High", 1.0),
    ("xhigh", "XHigh", 1.0),
    ("max", "Max", 2.0),
    ("ultra", "Ultra", 1.0),
    ("auto", "Auto", 1.0),
    ("unknown", "Unknown", 1.0),
)
REASONING_EFFORT_MULTIPLIER_DEFINITIONS = tuple(
    {
        "key": "user_quota.reasoning_multiplier.{}".format(effort),
        "group": "推理强度策略",
        "label": "{} 推理强度倍率".format(label),
        "description": (
            "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。"
        ),
        "type": "number",
        "default": default,
        "min": 0.1,
        "max": 10.0,
        "unit": "倍",
        "apply_mode": "quota",
    }
    for effort, label, default in REASONING_EFFORT_MULTIPLIER_DEFAULTS
)
REASONING_EFFORT_COLOR_DEFAULTS = (
    ("none", "None", "#7d8490"),
    ("minimal", "Minimal", "#84929a"),
    ("low", "Low", "#4b8ccf"),
    ("medium", "Medium", "#7653a6"),
    ("high", "High", "#2f73d9"),
    ("xhigh", "XHigh", "#5965c7"),
    ("max", "Max", "#b2731e"),
    ("ultra", "Ultra", "#9b5f9d"),
    ("auto", "Auto", "#5e708a"),
    ("unknown", "Unknown", "#687287"),
)
REASONING_EFFORT_COLOR_DEFINITIONS = tuple(
    {
        "key": "admin.account_usage.reasoning_effort_color.{}".format(effort),
        "group": "推理强度策略",
        "label": "{} 推理强度颜色".format(label),
        "description": "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。",
        "type": "color",
        "default": default,
        "apply_mode": "live",
    }
    for effort, label, default in REASONING_EFFORT_COLOR_DEFAULTS
)
CONFIG_DEFINITIONS = (
    {
        "key": "branding.product_name",
        "group": "品牌与身份",
        "label": "产品名称",
        "description": "Portal、使用中心、管理中心和通知中显示的完整名称。",
        "type": "text",
        "default": "Codex CPA Cluster",
        "min_length": 2,
        "max_length": 64,
        "apply_mode": "live",
    },
    {
        "key": "branding.short_name",
        "group": "品牌与身份",
        "label": "产品简称",
        "description": "客户端 Provider、紧凑导航和导出配置中使用的名称。",
        "type": "text",
        "default": "Codex CPA",
        "min_length": 2,
        "max_length": 32,
        "apply_mode": "live",
    },
    {
        "key": "branding.environment_label",
        "group": "品牌与身份",
        "label": "环境说明",
        "description": "入口页显示的环境或访问范围说明；可以留空。",
        "type": "optional_text",
        "default": "Self-hosted service",
        "max_length": 64,
        "apply_mode": "live",
    },
    {
        "key": "branding.public_base_url",
        "group": "品牌与身份",
        "label": "公开访问地址",
        "description": "通知及 Codex、Claude Code、CC Switch 导出使用的 HTTP(S) 根地址；协议按此值原样使用，留空时使用浏览器当前来源。",
        "type": "base_url",
        "default": "",
        "apply_mode": "live",
    },
    {
        "key": "identity.allowed_email_domains",
        "group": "品牌与身份",
        "label": "允许的邮箱域名",
        "description": "支持多个域名，使用逗号分隔；创建用户前必须至少配置一个。",
        "type": "domain_list",
        "default": [],
        "apply_mode": "live",
    },
    {
        "key": "identity.key_prefix",
        "group": "品牌与身份",
        "label": "新 Key 前缀",
        "description": "必须以下划线结尾；只影响之后创建或轮换的 Key，既有 Key 保持有效。",
        "type": "key_prefix",
        "default": "cpa_",
        "apply_mode": "live",
    },
    {
        "key": "portal.provider_name",
        "group": "品牌与身份",
        "label": "客户端 Provider 名称",
        "description": "Codex、CC Switch 等客户端配置中显示的 Provider 名称。",
        "type": "text",
        "default": "Codex CPA",
        "min_length": 2,
        "max_length": 48,
        "apply_mode": "live",
    },
    {
        "key": "portal.api_key_env",
        "group": "品牌与身份",
        "label": "客户端 Key 环境变量",
        "description": "使用中心生成的 Shell 配置所使用的环境变量名。",
        "type": "env_name",
        "default": "CPA_API_KEY",
        "apply_mode": "live",
    },
    {
        "key": "portal.default_model",
        "group": "品牌与身份",
        "label": "客户端默认模型",
        "description": "使用中心生成的 Codex 和 Claude Code 配置默认模型。",
        "type": "text",
        "default": "gpt-5.6-sol",
        "min_length": 1,
        "max_length": 128,
        "apply_mode": "live",
    },
    {
        "key": "cpa.proxy_enabled",
        "group": "CPA 请求",
        "label": "启用默认上游代理",
        "description": "启用后，所有选择“继承默认”的 CPA 使用默认代理；账号自定义代理或强制直连优先。",
        "type": "boolean",
        "default": False,
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.proxy_url",
        "group": "CPA 请求",
        "label": "默认上游代理 URL",
        "description": "支持 HTTP、HTTPS、SOCKS5；加密保存。仅作用于选择“继承默认”的 CPA。",
        "type": "proxy_url_secret",
        "default": "",
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.request_retry",
        "group": "CPA 请求",
        "label": "请求重试次数",
        "description": "单次上游请求失败后的重试次数。",
        "type": "integer",
        "default": 2,
        "min": 0,
        "max": 10,
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.disable_image_generation",
        "group": "CPA 请求",
        "label": "图片工具策略",
        "description": (
            "推荐仅在普通对话中禁用 CPA hosted 图片工具，避免与 Codex "
            "image_gen 命名空间冲突，同时保留专用图片生成接口。"
        ),
        "type": "choice",
        "default": "chat",
        "choices": [
            {"value": "chat", "label": "仅普通对话禁用（推荐）"},
            {"value": "true", "label": "全部禁用"},
            {"value": "false", "label": "全部启用"},
        ],
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.max_retry_credentials",
        "group": "CPA 请求",
        "label": "最大重试凭据数",
        "description": "一次请求最多切换尝试的 OAuth 凭据数量。",
        "type": "integer",
        "default": 1,
        "min": 1,
        "max": 10,
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.max_retry_interval",
        "group": "CPA 请求",
        "label": "最大重试等待",
        "description": "等待临时冷却凭据后再次重试的最长时间。",
        "type": "integer",
        "default": 12,
        "min": 1,
        "max": 300,
        "unit": "秒",
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.transient_error_cooldown_seconds",
        "group": "CPA 请求",
        "label": "临时错误冷却",
        "description": "上游 408/500/502/503/504 后暂停使用当前凭据的时间。",
        "type": "integer",
        "default": 10,
        "min": 1,
        "max": 300,
        "unit": "秒",
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.session_affinity",
        "group": "CPA 请求",
        "label": "会话亲和",
        "description": "同一会话优先沿用原有上游凭据。",
        "type": "boolean",
        "default": True,
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.session_affinity_ttl",
        "group": "CPA 请求",
        "label": "会话亲和有效期",
        "description": "支持 30s、5m、1h、7d 等格式。",
        "type": "duration",
        "default": "1h",
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.debug",
        "group": "CPA 请求",
        "label": "调试日志",
        "description": "仅排障时启用，可能显著增加日志量。",
        "type": "boolean",
        "default": False,
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.logging_to_file",
        "group": "CPA 请求",
        "label": "写入 CPA 日志文件",
        "description": "让业务 CPA 将运行日志写入各自日志目录，并由容量上限自动清理。",
        "type": "boolean",
        "default": True,
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.logs_max_total_size_mb",
        "group": "CPA 请求",
        "label": "单 CPA 日志容量上限",
        "description": "每个业务 CPA 日志目录的总容量上限，超出后由 CPA 删除最旧日志。",
        "type": "integer",
        "default": 64,
        "min": 16,
        "max": 1024,
        "unit": "MiB",
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.error_logs_max_files",
        "group": "CPA 请求",
        "label": "单 CPA 错误文件上限",
        "description": "每个业务 CPA 最多保留的请求错误日志文件数量。",
        "type": "integer",
        "default": 10,
        "min": 1,
        "max": 100,
        "unit": "个",
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.usage_statistics_enabled",
        "group": "用量与额度",
        "label": "官方用量事件",
        "description": "关闭后用户 Token 用量采集将停止获得新事件。",
        "type": "boolean",
        "default": True,
        "apply_mode": "accounts",
    },
    {
        "key": "cpa.usage_queue_retention_seconds",
        "group": "用量与额度",
        "label": "用量队列保留时间",
        "description": "采集器中断时，CPA 最长保留用量事件的秒数。",
        "type": "integer",
        "default": 3600,
        "min": 60,
        "max": 604800,
        "unit": "秒",
        "apply_mode": "accounts",
    },
    {
        "key": "usage.quota_cache_seconds",
        "group": "用量与额度",
        "label": "官方额度缓存",
        "description": "管理中心缓存各 CPA 官方额度的时间。",
        "type": "integer",
        "default": 60,
        "min": 30,
        "max": 3600,
        "unit": "秒",
        "apply_mode": "live",
    },
    {
        "key": "usage.upstream_timeout_seconds",
        "group": "用量与额度",
        "label": "官方接口超时",
        "description": "读取额度及执行 Full reset 时的上游等待上限。",
        "type": "integer",
        "default": 20,
        "min": 5,
        "max": 120,
        "unit": "秒",
        "apply_mode": "live",
    },
    {
        "key": "account_failover.mode",
        "group": "账号自动切换",
        "label": "自动切换模式",
        "description": "关闭时不检查；观察模式只生成计划；自动模式在账号官方周额度耗尽后批量迁移路由。",
        "type": "choice",
        "default": "active",
        "choices": [
            {"value": "off", "label": "关闭"},
            {"value": "observe", "label": "观察"},
            {"value": "active", "label": "自动执行"},
        ],
        "apply_mode": "live",
    },
    {
        "key": "account_failover.poll_seconds",
        "group": "账号自动切换",
        "label": "额度检查间隔",
        "description": "自动切换独立检查官方账号周额度的周期；不依赖企业微信通知开关。",
        "type": "integer",
        "default": 60,
        "min": 30,
        "max": 3600,
        "unit": "秒",
        "apply_mode": "live",
    },
    {
        "key": "account_failover.reserve_percent",
        "group": "账号自动切换",
        "label": "目标账号安全余量",
        "description": "剩余额度不高于该比例的账号不接收自动迁入用户。",
        "type": "number",
        "default": 5.0,
        "min": 0,
        "max": 50,
        "unit": "%",
        "apply_mode": "live",
    },
    {
        "key": "account_failover.stale_after_seconds",
        "group": "账号自动切换",
        "label": "额度数据失效时间",
        "description": "超过该时间未成功刷新官方额度时停止自动迁移，保留现有路由。",
        "type": "integer",
        "default": 120,
        "min": 60,
        "max": 7200,
        "unit": "秒",
        "apply_mode": "live",
    },
    {
        "key": "user_quota.default_weekly_tokens",
        "group": "用户额度",
        "label": "用户周额度系统默认值",
        "description": "按用户邮箱汇总全部 CPA 的自然周加权 Token；留空表示默认不限额，用户单独策略优先。",
        "type": "nullable_integer",
        "default": None,
        "min": 1,
        "max": 1000000000000,
        "unit": "Token",
        "apply_mode": "quota",
    },
    {
        "key": "user_quota.timezone",
        "group": "用户额度",
        "label": "用户自然周时区",
        "description": "用户周额度和今日用量的日期边界；修改后会按新时区重建周聚合。",
        "type": "timezone",
        "default": "UTC",
        "apply_mode": "collector",
    },
    {
        "key": "user_quota.fail_open_after_seconds",
        "group": "用户额度",
        "label": "额度故障放行等待",
        "description": "采集异常时继续使用最后有效快照；超过该时长后放行新请求并记录告警。",
        "type": "integer",
        "default": 300,
        "min": 30,
        "max": 3600,
        "unit": "秒",
        "apply_mode": "quota",
    },
    *REASONING_EFFORT_MULTIPLIER_DEFINITIONS,
    *REASONING_EFFORT_COLOR_DEFINITIONS,
    {
        "key": "notification.enabled",
        "group": "企业微信通知",
        "label": "启用企业微信通知",
        "description": "启用后按设定时间发送账号额度表格，并执行周额度阈值检查。",
        "type": "boolean",
        "default": False,
        "apply_mode": "live",
    },
    {
        "key": "notification.timezone",
        "group": "企业微信通知",
        "label": "通知时区",
        "description": "定时发送和额度刷新时间使用的 IANA 时区。",
        "type": "timezone",
        "default": "UTC",
        "apply_mode": "live",
    },
    {
        "key": "notification.daily_times",
        "group": "企业微信通知",
        "label": "每日发送时间",
        "description": "多个 HH:MM 使用逗号分隔，保存时自动去重排序。",
        "type": "time_list",
        "default": "09:00,14:00,18:00",
        "apply_mode": "live",
    },
    {
        "key": "notification.schedule_grace_minutes",
        "group": "企业微信通知",
        "label": "定时补发窗口",
        "description": "服务在发送时刻后恢复时，允许补发报告的分钟数。",
        "type": "integer",
        "default": 15,
        "min": 0,
        "max": 120,
        "unit": "分钟",
        "apply_mode": "live",
    },
    {
        "key": "notification.quota_alert_enabled",
        "group": "企业微信通知",
        "label": "启用周额度预警",
        "description": "每个账号额度窗口首次达到阈值时发送一次；恢复到阈值以下后重新布防。",
        "type": "boolean",
        "default": True,
        "apply_mode": "live",
    },
    {
        "key": "notification.weekly_threshold_percent",
        "group": "企业微信通知",
        "label": "周额度预警阈值",
        "description": "各账号、各周额度窗口独立判断的已使用百分比。",
        "type": "number",
        "default": 90.0,
        "min": 1,
        "max": 100,
        "unit": "%",
        "apply_mode": "live",
    },
    {
        "key": "portal.session_ttl_seconds",
        "group": "会话与采集",
        "label": "使用中心登录有效期",
        "description": "只影响保存后新创建的用户登录会话。",
        "type": "integer",
        "default": 43200,
        "min": 3600,
        "max": 43200,
        "unit": "秒",
        "apply_mode": "live",
    },
    {
        "key": "collector.interval_seconds",
        "group": "会话与采集",
        "label": "采集轮询间隔",
        "description": "用量采集器两轮扫描之间的等待时间。",
        "type": "number",
        "default": 2.0,
        "min": 0.5,
        "max": 60,
        "unit": "秒",
        "apply_mode": "collector",
    },
    {
        "key": "collector.batch_size",
        "group": "会话与采集",
        "label": "单批采集数量",
        "description": "每次从单个 CPA 用量队列读取的最大事件数。",
        "type": "integer",
        "default": 100,
        "min": 1,
        "max": 500,
        "apply_mode": "collector",
    },
    {
        "key": "accounts.port_start",
        "group": "账号供应",
        "label": "新账号端口起点",
        "description": "只影响后续新建 CPA，现有账号端口保持不变。",
        "type": "integer",
        "default": ACCOUNT_PORT_START,
        "min": 1024,
        "max": 65535,
        "apply_mode": "future",
    },
    {
        "key": "accounts.port_end",
        "group": "账号供应",
        "label": "新账号端口终点",
        "description": "只影响后续新建 CPA，必须不小于端口起点。",
        "type": "integer",
        "default": ACCOUNT_PORT_END,
        "min": 1024,
        "max": 65535,
        "apply_mode": "future",
    },
    {
        "key": "accounts.listen_address",
        "group": "部署环境",
        "label": "业务 CPA 监听地址",
        "description": "固定为宿主机回环地址；业务 CPA 只能由本机发布检查或 Docker 内网访问。",
        "type": "ip",
        "default": "127.0.0.1",
        "apply_mode": "deployment",
    },
    {
        "key": "runtime.cliproxy_image",
        "group": "部署环境",
        "label": "CLIProxyAPI 镜像",
        "description": "作为更新通道；账号管理拉取后识别真实版本，验证通过才固定为不可变镜像。",
        "type": "image",
        "default": "docker.m.daocloud.io/eceasy/cli-proxy-api:v7.1.23",
        "apply_mode": "deployment",
    },
    {
        "key": "runtime.gateway_image",
        "group": "部署环境",
        "label": "Gateway 基础镜像",
        "description": "仅供源码检出环境本地构建使用；版本化发布镜像由发布工作站在打包时确定。",
        "type": "image",
        "default": DEFAULT_GATEWAY_BASE_IMAGE,
        "digest_required": True,
        "apply_mode": "deployment",
    },
    {
        "key": "runtime.admin_base_image",
        "group": "部署环境",
        "label": "Admin 构建基础镜像",
        "description": "仅供源码检出环境本地构建使用；版本化发布镜像由发布工作站在打包时确定。",
        "type": "image",
        "default": DEFAULT_ADMIN_BASE_IMAGE,
        "digest_required": True,
        "apply_mode": "deployment",
    },
    {
        "key": "gateway.listen_address",
        "group": "部署环境",
        "label": "网关监听地址",
        "description": "固定为宿主机回环地址；公网流量由同一 Docker 网络中的 TLS 入口转发。",
        "type": "ip",
        "default": "127.0.0.1",
        "apply_mode": "deployment",
    },
    {
        "key": "management.listen_address",
        "group": "部署环境",
        "label": "Management 监听地址",
        "description": "固定为宿主机回环地址；Management API 不直接暴露到公网。",
        "type": "ip",
        "default": "127.0.0.1",
        "apply_mode": "deployment",
    },
    {
        "key": "gateway.port",
        "group": "部署环境",
        "label": "网关宿主机端口",
        "description": "由 SQLite 自动生成 Compose 投影；修改后管理中心入口地址也会变化。",
        "type": "integer",
        "default": 18317,
        "min": 1024,
        "max": 65535,
        "apply_mode": "deployment",
    },
    {
        "key": "gateway.internal_port",
        "group": "部署环境",
        "label": "网关内部探针端口",
        "description": "仅绑定宿主机回环地址，供发布验收读取快照和验证真实路由。",
        "type": "integer",
        "default": 18316,
        "min": 1024,
        "max": 65535,
        "apply_mode": "deployment",
    },
    {
        "key": "management.port",
        "group": "部署环境",
        "label": "Management 宿主机端口",
        "description": "仅绑定宿主机回环地址，供本机管理与发布检查使用。",
        "type": "integer",
        "default": 18318,
        "min": 1024,
        "max": 65535,
        "apply_mode": "deployment",
    },
    {
        "key": "delivery.gateway_drain_timeout_seconds",
        "group": "部署环境",
        "label": "Gateway 排空超时",
        "description": "蓝绿发布等待旧 Gateway 长连接结束的最长时间。",
        "type": "integer",
        "default": 3600,
        "min": 30,
        "max": 7200,
        "unit": "秒",
        "apply_mode": "deployment",
    },
    {
        "key": "delivery.release_metadata_image",
        "group": "部署环境",
        "label": "发布更新通道",
        "description": "Admin 只读检查项目新版本所使用的 metadata 镜像；可留空关闭提醒。",
        "type": "optional_image",
        "default": "",
        "apply_mode": "deployment",
    },
)
CONFIG_DEFINITION_BY_KEY = {item["key"]: item for item in CONFIG_DEFINITIONS}
USER_KEY_UUID_RE = re.compile(
    r"^[a-z][a-z0-9_]{1,31}_[a-z0-9_]+_"
    r"[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)


class ControlPlane:
    def __init__(self, root):
        self.root = Path(root).resolve()
        self.public_accounts_path = self.root / "state" / "public" / "accounts.json"
        self.public_site_config_path = self.root / "state" / "public" / "site-config.json"
        self.accounts_compose_path = self.root / "compose.accounts.yml"
        self.compose_env_path = self.root / COMPOSE_ENV_PATH
        self.issued_path = self.root / "secrets" / "issued-keys.tsv"
        self.access_log = self.root / "logs" / "gateway" / "access.tsv"
        self.snapshot_dir = self.root / "state" / "gateway"
        self.edge_state_dir = self.root / "state" / "edge"
        self.gateway_key_map_path = self.snapshot_dir / "keys.map.conf"
        self.gateway_accounts_map_path = self.snapshot_dir / "accounts.map.conf"
        self.gateway_backends_map_path = self.snapshot_dir / "backends.map.conf"
        self.auth_snapshot_path = self.snapshot_dir / "auth-snapshot.json"
        self.quota_snapshot_path = self.snapshot_dir / "quota-snapshot.json"
        self.quota_heartbeat_path = self.snapshot_dir / "quota-heartbeat.json"
        self._access_log_lock = threading.RLock()
        self._access_log_identity = None
        self._access_log_offset = 0
        self._access_log_prefix = b""
        self._access_log_rows = []
        self._access_log_retention_seconds = 60 * 60
        self._access_log_last_now = None
        self.store = ControlPlaneStore(self.root)

    def ensure_layout(self):
        legacy_environment = read_env(self.root / ".env")
        relatives = [
            "configs",
            "logs/gateway",
            "secrets",
            "state",
            "state/gateway",
            "state/edge",
            "state/public",
        ]
        for account in self.accounts():
            relatives.extend(["auth/{}".format(account), "logs/{}".format(account)])
        for relative in relatives:
            (self.root / relative).mkdir(parents=True, exist_ok=True)
        os.chmod(self.root / "state", 0o700)
        os.chmod(self.snapshot_dir, 0o750)
        os.chmod(self.edge_state_dir, 0o750)
        if os.geteuid() == 0:
            os.chown(self.snapshot_dir, -1, GATEWAY_SNAPSHOT_GID)
        os.chmod(self.root / "secrets", 0o700)
        self.migrate_legacy_environment(legacy_environment)
        ensure_active_slot(
            self.root,
            fallback=legacy_environment.get("GATEWAY_ACTIVE_SLOT", "blue"),
        )
        self.render_compose_environment()

    def migrate_legacy_environment(self, environment=None):
        """Import the former mixed .env contract, then retain only host bootstrap keys."""
        path = self.root / ".env"
        environment = dict(environment if environment is not None else read_env(path))
        deployment = self.deployment_runtime_state()
        applied_deployment = dict(deployment.get("applied") or {})
        legacy_deployment_values = {}
        for env_key, field in LEGACY_DEPLOYMENT_ENV_KEYS.items():
            if applied_deployment.get(field):
                continue
            value = str(environment.get(env_key, "") or "").strip()
            if not value:
                continue
            if field == "version":
                if not SEMANTIC_VERSION_RE.fullmatch(value):
                    raise ValueError("旧 .env 中的 RELEASE_VERSION 不是语义化版本")
            else:
                value = self._normalize_configuration_value(
                    CONFIG_DEFINITION_BY_KEY["runtime.cliproxy_image"], value
                )
            legacy_deployment_values[field] = value
        stored = self._read_stored_configuration()
        migrated_settings = []
        for key, env_key in LEGACY_ENV_SETTING_KEYS.items():
            if key in stored or env_key not in environment:
                continue
            raw = environment[env_key]
            migrations = PINNED_DEFAULT_IMAGE_MIGRATIONS.get(key)
            if migrations is not None:
                raw = migrations.get(raw, raw)
            if key in {
                "accounts.listen_address",
                "management.listen_address",
                "gateway.listen_address",
            } and str(raw).strip() in {"0.0.0.0", "::"}:
                raw = "127.0.0.1"
            stored[key] = self._normalize_configuration_value(
                CONFIG_DEFINITION_BY_KEY[key], raw
            )
            migrated_settings.append(key)

        if migrated_settings:
            effective = {}
            for definition in CONFIG_DEFINITIONS:
                key = definition["key"]
                raw = (
                    self.store.read_secret(DEFAULT_PROXY_SECRET, "")
                    if key == "cpa.proxy_url"
                    else stored.get(key, definition["default"])
                )
                effective[key] = self._normalize_configuration_value(definition, raw)
            self._validate_configuration(effective)
            self._write_configuration(stored)

        migrated_deployment = []
        for field, value in legacy_deployment_values.items():
            if value and not applied_deployment.get(field):
                applied_deployment[field] = value
                migrated_deployment.append(field)
        if migrated_deployment:
            applied_deployment.setdefault("migrated_at", int(time.time()))
            deployment["applied"] = applied_deployment
            self.store.write_runtime_state("deployment", deployment)

        bootstrap = {
            "DEPLOY_ROOT": str(
                environment.get("DEPLOY_ROOT")
                or os.environ.get("DEPLOY_ROOT")
                or self.root
            ),
            **{
                key: str(
                    environment.get(key)
                    or os.environ.get(key)
                    or default
                )
                for key, default in BOOTSTRAP_ENV_DEFAULTS.items()
            },
        }
        non_bootstrap_keys = sorted(set(environment) - set(BOOTSTRAP_ENV_KEYS))
        known_keys = set(LEGACY_ENV_SETTING_KEYS.values()) | set(
            LEGACY_DEPLOYMENT_ENV_KEYS
        ) | {"GATEWAY_ACTIVE_SLOT"}
        unmapped_keys = sorted(set(non_bootstrap_keys) - known_keys)
        backup_path = None
        if path.exists() and non_bootstrap_keys:
            backup_path = self.root / LEGACY_ENV_BACKUP_PATH
            if backup_path.exists():
                backup_path = backup_path.with_name(
                    "{}.{}".format(backup_path.name, time.time_ns())
                )
            self._atomic_text(
                backup_path,
                path.read_text(encoding="utf-8"),
                0o600,
            )
        content = "# Host/Compose bootstrap only. Runtime values are generated in state/compose.env.\n"
        content += "\n".join(
            "{}={}".format(key, bootstrap[key]) for key in BOOTSTRAP_ENV_KEYS
        ) + "\n"
        if not path.exists() or path.read_text(encoding="utf-8") != content:
            self._atomic_text(path, content, 0o600)
        else:
            os.chmod(path, 0o600)

        if migrated_settings or migrated_deployment or non_bootstrap_keys:
            self.store.write_runtime_state(
                "legacy_environment_migration",
                {
                    "migrated_at": int(time.time()),
                    "settings": migrated_settings,
                    "deployment_fields": migrated_deployment,
                    "unmapped_keys": unmapped_keys,
                    "backup_path": (
                        str(backup_path.relative_to(self.root)) if backup_path else ""
                    ),
                },
            )
        return {
            "settings": migrated_settings,
            "deployment_fields": migrated_deployment,
            "unmapped_keys": unmapped_keys,
            "backup_path": str(backup_path) if backup_path else "",
        }

    @contextlib.contextmanager
    def runtime_operation_lock(self, operation):
        """Reject Docker mutations that overlap a target-local release."""
        path = self.root / RUNTIME_OPERATION_LOCK_PATH
        path.parent.mkdir(parents=True, exist_ok=True)
        descriptor = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
        locked = False
        try:
            os.chmod(path, 0o600)
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                locked = True
            except OSError as error:
                if error.errno not in (errno.EACCES, errno.EAGAIN):
                    raise
                raise ValueError(
                    "另一个发布或运行时操作正在进行，暂时不能执行{}".format(
                        operation
                    )
                ) from error
            yield
        finally:
            if locked:
                fcntl.flock(descriptor, fcntl.LOCK_UN)
            os.close(descriptor)

    @contextlib.contextmanager
    def compose_environment_lock(self):
        """Serialize projection reads and the atomic replacement across processes."""
        path = self.root / COMPOSE_ENV_LOCK_PATH
        path.parent.mkdir(parents=True, exist_ok=True)
        descriptor = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
        try:
            os.chmod(path, 0o600)
            fcntl.flock(descriptor, fcntl.LOCK_EX)
            yield
        finally:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_UN)
            finally:
                os.close(descriptor)

    @staticmethod
    def configuration_definitions():
        return [dict(item) for item in CONFIG_DEFINITIONS]

    def public_site_configuration(self):
        values = self.configuration()["values"]
        logo = self.store.branding_asset("logo")
        return {
            "version": CONFIG_VERSION,
            "product_name": values["branding.product_name"],
            "short_name": values["branding.short_name"],
            "environment_label": values["branding.environment_label"],
            "public_base_url": values["branding.public_base_url"],
            "provider_name": values["portal.provider_name"],
            "api_key_env": values["portal.api_key_env"],
            "default_model": values["portal.default_model"],
            "logo": {
                "custom": bool(logo),
                "url": "/branding/logo" if logo else "/portal/assets/codex-cpa-cluster-logo.svg",
                "content_type": logo["content_type"] if logo else "image/svg+xml",
                "sha256": logo["sha256"] if logo else "",
                "updated_at": logo["updated_at"] if logo else None,
            },
        }

    def render_public_site_configuration(self):
        """Publish the non-secret browser configuration independently of a full render."""
        self._atomic_text(
            self.public_site_config_path,
            json.dumps(self.public_site_configuration(), ensure_ascii=False, indent=2) + "\n",
            0o644,
        )
        return self.public_site_configuration()

    def update_logo(self, filename, content_type, content):
        validated = validate_logo(filename, content_type, content)
        asset = self.store.write_branding_asset(**validated)
        self.render_public_site_configuration()
        return {key: value for key, value in asset.items() if key != "content"}

    def delete_logo(self):
        existed = self.store.branding_asset("logo") is not None
        self.store.delete_branding_asset("logo")
        self.render_public_site_configuration()
        return existed

    @staticmethod
    def _normalize_configuration_value(definition, value):
        value_type = definition["type"]
        key = definition["key"]
        if value_type == "boolean":
            if isinstance(value, str) and value.strip().lower() in (
                "true", "false", "1", "0", "yes", "no", "on", "off"
            ):
                return value.strip().lower() in ("true", "1", "yes", "on")
            if not isinstance(value, bool):
                raise ValueError("{} 必须为布尔值".format(definition["label"]))
            return value
        if value_type == "integer":
            if isinstance(value, bool):
                raise ValueError("{} 必须为整数".format(definition["label"]))
            if isinstance(value, float) and (
                not math.isfinite(value) or not value.is_integer()
            ):
                raise ValueError("{} 必须为整数".format(definition["label"]))
            if isinstance(value, str) and not re.fullmatch(r"[+-]?\d+", value.strip()):
                raise ValueError("{} 必须为整数".format(definition["label"]))
            try:
                normalized = int(value)
            except (TypeError, ValueError):
                raise ValueError("{} 必须为整数".format(definition["label"]))
            if normalized < definition.get("min", normalized) or normalized > definition.get("max", normalized):
                raise ValueError(
                    "{} 必须在 {} 至 {} 之间".format(
                        definition["label"], definition.get("min"), definition.get("max")
                    )
                )
            return normalized
        if value_type == "nullable_integer":
            if value is None or (isinstance(value, str) and not value.strip()):
                return None
            integer_definition = dict(definition)
            integer_definition["type"] = "integer"
            return ControlPlane._normalize_configuration_value(integer_definition, value)
        if value_type == "number":
            if isinstance(value, bool):
                raise ValueError("{} 必须为数字".format(definition["label"]))
            try:
                normalized = float(value)
            except (TypeError, ValueError):
                raise ValueError("{} 必须为数字".format(definition["label"]))
            if not math.isfinite(normalized):
                raise ValueError("{} 必须为有限数字".format(definition["label"]))
            if normalized < definition.get("min", normalized) or normalized > definition.get("max", normalized):
                raise ValueError(
                    "{} 必须在 {} 至 {} 之间".format(
                        definition["label"], definition.get("min"), definition.get("max")
                    )
                )
            return normalized

        normalized = str(value or "").strip()
        if value_type in ("text", "optional_text"):
            if value_type == "text" and not normalized:
                raise ValueError("{}不能为空".format(definition["label"]))
            if len(normalized) < definition.get("min_length", 0):
                raise ValueError(
                    "{}至少需要 {} 个字符".format(
                        definition["label"], definition["min_length"]
                    )
                )
            if len(normalized) > definition.get("max_length", len(normalized)):
                raise ValueError(
                    "{}不能超过 {} 个字符".format(
                        definition["label"], definition["max_length"]
                    )
                )
            if any(ord(character) < 32 for character in normalized):
                raise ValueError("{}不能包含控制字符".format(definition["label"]))
            return normalized
        if value_type == "domain_list":
            raw_values = value if isinstance(value, (list, tuple)) else re.split(r"[,，\s]+", normalized)
            domains = []
            for raw_domain in raw_values:
                domain = str(raw_domain or "").strip().lower().lstrip("@")
                if not domain:
                    continue
                if (
                    len(domain) > 253
                    or ".." in domain
                    or not re.fullmatch(
                        r"(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+"
                        r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?",
                        domain,
                    )
                ):
                    raise ValueError("{}包含无效域名：{}".format(definition["label"], domain))
                if domain not in domains:
                    domains.append(domain)
            return domains
        if value_type == "host_list":
            raw_values = value if isinstance(value, (list, tuple)) else re.split(r"[,，\s]+", normalized)
            hosts = []
            for raw_host in raw_values:
                host = str(raw_host or "").strip().lower().rstrip(".")
                if not host:
                    continue
                try:
                    normalized_host = str(ipaddress.ip_address(host))
                except ValueError:
                    if (
                        len(host) > 253
                        or ".." in host
                        or not re.fullmatch(
                            r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?"
                            r"(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*",
                            host,
                        )
                    ):
                        raise ValueError("{}包含无效主机：{}".format(definition["label"], host))
                    normalized_host = host
                if normalized_host not in hosts:
                    hosts.append(normalized_host)
            return hosts
        if value_type == "hostname":
            if not normalized:
                return ""
            host = normalized.lower().rstrip(".")
            try:
                return str(ipaddress.ip_address(host))
            except ValueError:
                if (
                    len(host) > 253
                    or ".." in host
                    or not re.fullmatch(
                        r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?"
                        r"(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*",
                        host,
                    )
                ):
                    raise ValueError("{}必须为有效主机名或 IP".format(definition["label"]))
                return host
        if value_type == "key_prefix":
            prefix = normalized.lower()
            if not re.fullmatch(r"[a-z][a-z0-9_]{1,30}_", prefix):
                raise ValueError("{}必须为 3-32 位小写字母、数字或下划线，并以下划线结尾".format(definition["label"]))
            return prefix
        if value_type == "env_name":
            if not re.fullmatch(r"[A-Z][A-Z0-9_]{1,63}", normalized):
                raise ValueError("{}必须为有效的大写环境变量名".format(definition["label"]))
            return normalized
        if value_type == "choice":
            allowed = {
                str(item["value"])
                for item in definition.get("choices", [])
            }
            if normalized not in allowed:
                raise ValueError(
                    "{} 必须选择以下值之一：{}".format(
                        definition["label"], ", ".join(sorted(allowed))
                    )
                )
            return normalized
        if value_type == "color":
            if not re.fullmatch(r"#[0-9A-Fa-f]{6}", normalized):
                raise ValueError(
                    "{} 必须使用 #RRGGBB 颜色格式".format(definition["label"])
                )
            return normalized.lower()
        if value_type in ("url", "base_url", "proxy_url_secret"):
            if not normalized:
                return ""
            if any(character.isspace() or ord(character) < 32 for character in normalized):
                raise ValueError("{} 不得包含空白或控制字符".format(definition["label"]))
            parsed = urllib.parse.urlsplit(normalized)
            allowed_schemes = (
                ("http", "https", "socks5")
                if value_type == "proxy_url_secret"
                else ("http", "https")
            )
            if parsed.scheme.lower() not in allowed_schemes or not parsed.hostname:
                raise ValueError(
                    "{} 必须为有效的 {} URL".format(
                        definition["label"],
                        "HTTP、HTTPS 或 SOCKS5" if value_type == "proxy_url_secret" else "HTTP(S)",
                    )
                )
            if value_type != "proxy_url_secret" and (parsed.username or parsed.password):
                raise ValueError("{} 不得包含账号或密码".format(definition["label"]))
            try:
                parsed.port
            except ValueError:
                raise ValueError("{} 包含无效端口".format(definition["label"]))
            if value_type in ("base_url", "proxy_url_secret") and (
                parsed.path not in ("", "/") or parsed.query or parsed.fragment
            ):
                raise ValueError("{} 不得包含查询参数或片段".format(definition["label"]))
            return normalized.rstrip("/")
        if value_type == "duration":
            seconds = parse_duration(normalized)
            if seconds < 30 or seconds > 30 * 24 * 60 * 60:
                raise ValueError("{} 必须在 30 秒至 30 天之间".format(definition["label"]))
            return normalized.lower()
        if value_type == "timezone":
            if not normalized or len(normalized) > 64:
                raise ValueError("{} 必须为有效 IANA 时区".format(definition["label"]))
            if ZoneInfo is not None:
                try:
                    ZoneInfo(normalized)
                except (ZoneInfoNotFoundError, ValueError):
                    raise ValueError("{} 必须为有效 IANA 时区".format(definition["label"]))
            else:
                if (
                    not re.fullmatch(r"[A-Za-z0-9._+-]+(?:/[A-Za-z0-9._+-]+)*", normalized)
                    or any(part in (".", "..") for part in normalized.split("/"))
                    or not any(
                        (Path(base) / normalized).is_file()
                        for base in ("/usr/share/zoneinfo", "/usr/share/lib/zoneinfo")
                    )
                ):
                    raise ValueError("{} 必须为有效 IANA 时区".format(definition["label"]))
            return normalized
        if value_type == "time_list":
            parts = [
                part
                for part in re.split(r"[,，\s]+", normalized)
                if part
            ]
            if not parts or len(parts) > 12:
                raise ValueError("{} 必须包含 1 至 12 个时间".format(definition["label"]))
            times = set()
            for part in parts:
                match = re.fullmatch(r"(\d{1,2}):(\d{2})", part)
                if not match:
                    raise ValueError("{} 必须使用 HH:MM 格式".format(definition["label"]))
                hour, minute = int(match.group(1)), int(match.group(2))
                if hour > 23 or minute > 59:
                    raise ValueError("{} 包含无效时间".format(definition["label"]))
                times.add("{:02d}:{:02d}".format(hour, minute))
            return ",".join(sorted(times))
        if value_type in ("image", "optional_image"):
            if value_type == "optional_image" and not normalized:
                return ""
            if not normalized or len(normalized) > 255 or not re.fullmatch(r"[A-Za-z0-9._:/@-]+", normalized):
                raise ValueError("{} 的镜像名称无效".format(definition["label"]))
            if definition.get("digest_required") and not re.fullmatch(
                r"[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}", normalized
            ):
                raise ValueError(
                    "{} 必须使用 name:tag@sha256:digest 固定镜像".format(
                        definition["label"]
                    )
                )
            return normalized
        if value_type == "ip":
            try:
                address = ipaddress.ip_address(normalized)
            except ValueError:
                raise ValueError("{} 必须为有效 IPv4 地址".format(definition["label"]))
            if address.version != 4:
                raise ValueError("{} 当前仅支持 IPv4 地址".format(definition["label"]))
            return str(address)
        raise ValueError("未知配置类型：{} ({})".format(value_type, key))

    def _read_stored_configuration(self):
        values = self.store.read_settings()
        unknown = sorted(set(values) - set(CONFIG_DEFINITION_BY_KEY))
        if unknown:
            unexpected = sorted(set(unknown) - RETIRED_CONFIG_KEYS)
            if unexpected:
                raise ValueError(
                    "配置中心包含未知参数：{}".format(", ".join(unexpected))
                )
            values = {
                key: value
                for key, value in values.items()
                if key in CONFIG_DEFINITION_BY_KEY
            }
            self.store.write_settings(values)
        return values

    def _write_configuration(self, values):
        self.store.write_settings(values)

    def configuration(self):
        stored = self._read_stored_configuration()
        inferred = dict(stored)
        legacy_proxy_url = str(inferred.pop("cpa.proxy_url", "") or "").strip()
        if legacy_proxy_url:
            self.store.write_secret(DEFAULT_PROXY_SECRET, legacy_proxy_url)
            inferred.setdefault("cpa.proxy_enabled", True)
        for key, migrations in PINNED_DEFAULT_IMAGE_MIGRATIONS.items():
            if inferred.get(key) in migrations:
                inferred[key] = migrations[inferred[key]]
        for listen_key in (
            "accounts.listen_address",
            "management.listen_address",
            "gateway.listen_address",
        ):
            if inferred.get(listen_key) in ("0.0.0.0", "::"):
                inferred[listen_key] = "127.0.0.1"
        try:
            legacy_session_ttl = int(inferred.get("portal.session_ttl_seconds", 0))
        except (TypeError, ValueError):
            legacy_session_ttl = 0
        if legacy_session_ttl > 43200:
            inferred["portal.session_ttl_seconds"] = 43200
        records = None
        if "identity.allowed_email_domains" not in inferred:
            records = self.store.read_key_records()
            domains = sorted(
                {
                    item["user"].rsplit("@", 1)[1].lower()
                    for item in records
                    if "@" in str(item.get("user", ""))
                }
            )
            if domains:
                inferred["identity.allowed_email_domains"] = domains
        if "identity.key_prefix" not in inferred:
            records = records if records is not None else self.store.read_key_records()
            for item in records:
                key = str(item.get("key", ""))
                user_namespace = self._key_user_namespace(str(item.get("user", "")))
                match = re.search(
                    r"_([0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})$",
                    key,
                )
                suffix = "{}_".format(user_namespace)
                stem = key[: match.start() + 1] if match else ""
                if stem.endswith(suffix):
                    prefix = stem[: -len(suffix)]
                    if re.fullmatch(r"[a-z][a-z0-9_]{1,30}_", prefix):
                        inferred["identity.key_prefix"] = prefix
                        break
        if inferred != stored:
            self._write_configuration(inferred)
            stored = inferred
        values = {}
        for definition in CONFIG_DEFINITIONS:
            key = definition["key"]
            raw = (
                self.store.read_secret(DEFAULT_PROXY_SECRET, "")
                if key == "cpa.proxy_url"
                else stored.get(key, definition["default"])
            )
            image_migrations = PINNED_DEFAULT_IMAGE_MIGRATIONS.get(key)
            if image_migrations is not None:
                raw = image_migrations.get(raw, raw)
            values[key] = self._normalize_configuration_value(definition, raw)
        self._validate_configuration(values)
        return {"version": CONFIG_VERSION, "values": values}

    def redacted_configuration(self, payload=None):
        payload = payload or self.configuration()
        redacted = json.loads(json.dumps(payload))
        values = redacted.get("values")
        if isinstance(values, dict) and "cpa.proxy_url" in values:
            values["cpa.proxy_url"] = "***" if values["cpa.proxy_url"] else ""
        return redacted

    def _validate_configuration(self, values):
        for key in (
            "accounts.listen_address",
            "management.listen_address",
            "gateway.listen_address",
        ):
            if not ipaddress.ip_address(values[key]).is_loopback:
                raise ValueError("公网部署的服务监听地址必须使用宿主机回环地址")
        if values["accounts.port_start"] > values["accounts.port_end"]:
            raise ValueError("新账号端口起点不能大于终点")
        if values["gateway.port"] in range(
            values["accounts.port_start"], values["accounts.port_end"] + 1
        ):
            raise ValueError("网关端口不能位于新账号端口分配范围内")
        existing_ports = {
            int(item["port"])
            for item in self._read_account_records()
        }
        if values["gateway.port"] in existing_ports:
            raise ValueError("网关端口不能与现有业务 CPA 端口重复")
        if values["gateway.internal_port"] == values["gateway.port"]:
            raise ValueError("网关内部探针端口不能与公网网关端口相同")
        if values["gateway.internal_port"] in existing_ports:
            raise ValueError("网关内部探针端口不能与现有业务 CPA 端口重复")
        deployment_ports = {
            values["gateway.port"],
            values["gateway.internal_port"],
            values["management.port"],
        }
        if len(deployment_ports) != 3:
            raise ValueError("Gateway 公网、内部探针和 Management 端口不能重复")
        if values["management.port"] in existing_ports:
            raise ValueError("Management 端口不能与现有业务 CPA 端口重复")
        if values["account_failover.stale_after_seconds"] < values["account_failover.poll_seconds"]:
            raise ValueError("账号自动切换额度数据失效时间不能小于检查间隔")
        if values["cpa.proxy_enabled"] and not values["cpa.proxy_url"]:
            raise ValueError("启用默认上游代理前必须配置默认代理 URL")

    def update_configuration(self, changes):
        if not isinstance(changes, dict) or not changes:
            raise ValueError("请至少提交一个配置项")
        unknown = sorted(set(changes) - set(CONFIG_DEFINITION_BY_KEY))
        if unknown:
            raise ValueError("不支持的配置项：{}".format(", ".join(unknown)))
        stored = self._read_stored_configuration()
        current = self.configuration()["values"]
        updated = dict(current)
        stored_updated = dict(stored)
        for key, value in changes.items():
            normalized = self._normalize_configuration_value(
                CONFIG_DEFINITION_BY_KEY[key], value
            )
            updated[key] = normalized
            if key != "cpa.proxy_url":
                stored_updated[key] = normalized
        self._validate_configuration(updated)
        changed = [key for key in updated if updated[key] != current[key]]
        if changed:
            try:
                if "cpa.proxy_url" in changed:
                    if updated["cpa.proxy_url"]:
                        self.store.write_secret(DEFAULT_PROXY_SECRET, updated["cpa.proxy_url"])
                    else:
                        self.store.delete_secret(DEFAULT_PROXY_SECRET)
                self._write_configuration(stored_updated)
            except Exception:
                if "cpa.proxy_url" in changed:
                    if current["cpa.proxy_url"]:
                        self.store.write_secret(DEFAULT_PROXY_SECRET, current["cpa.proxy_url"])
                    else:
                        self.store.delete_secret(DEFAULT_PROXY_SECRET)
                raise
            if any(
                key.startswith(("branding.", "identity.", "portal."))
                for key in changed
            ):
                self.render_public_site_configuration()
        return {
            "before": current,
            "stored_before": stored,
            "values": updated,
            "changed": changed,
        }

    def replace_configuration(self, values):
        if not isinstance(values, dict):
            raise ValueError("恢复配置必须为对象")
        unknown = sorted(set(values) - set(CONFIG_DEFINITION_BY_KEY))
        if unknown:
            raise ValueError("恢复配置包含未知参数：{}".format(", ".join(unknown)))
        normalized_stored = {
            key: self._normalize_configuration_value(
                CONFIG_DEFINITION_BY_KEY[key], value
            )
            for key, value in values.items()
        }
        restored_proxy_url = normalized_stored.pop("cpa.proxy_url", UNSET)
        effective = {}
        for definition in CONFIG_DEFINITIONS:
            key = definition["key"]
            raw = (
                (
                    restored_proxy_url
                    if restored_proxy_url is not UNSET
                    else self.store.read_secret(DEFAULT_PROXY_SECRET, "")
                )
                if key == "cpa.proxy_url"
                else normalized_stored.get(key, definition["default"])
            )
            effective[key] = self._normalize_configuration_value(definition, raw)
        self._validate_configuration(effective)
        if restored_proxy_url is not UNSET:
            if restored_proxy_url:
                self.store.write_secret(DEFAULT_PROXY_SECRET, restored_proxy_url)
            else:
                self.store.delete_secret(DEFAULT_PROXY_SECRET)
        self._write_configuration(normalized_stored)
        self.render_public_site_configuration()

    def _validate_configuration_profile(self, payload):
        """Validate a profile without changing the authoritative configuration."""
        if not isinstance(payload, dict):
            raise ValueError("配置档案必须是 JSON 对象")
        unknown_sections = sorted(set(payload) - {"version", "values", "branding"})
        if unknown_sections:
            raise ValueError("配置档案包含未知区块：{}".format(", ".join(unknown_sections)))
        if int(payload.get("version", 1)) != 1:
            raise ValueError("不支持的配置档案版本")
        values = payload.get("values")
        if not isinstance(values, dict) or not values:
            raise ValueError("配置档案必须包含非空 values 对象")
        unknown_values = sorted(set(values) - set(CONFIG_DEFINITION_BY_KEY))
        if unknown_values:
            raise ValueError("不支持的配置项：{}".format(", ".join(unknown_values)))
        normalized_values = {
            key: self._normalize_configuration_value(CONFIG_DEFINITION_BY_KEY[key], value)
            for key, value in values.items()
        }
        effective = dict(self.configuration()["values"])
        effective.update(normalized_values)
        self._validate_configuration(effective)
        branding = payload.get("branding")
        validated_logo = None
        if branding is not None:
            if not isinstance(branding, dict):
                raise ValueError("配置档案 branding 必须是对象")
            unknown_branding = sorted(set(branding) - {"logo"})
            if unknown_branding:
                raise ValueError(
                    "配置档案 branding 包含未知参数：{}".format(
                        ", ".join(unknown_branding)
                    )
                )
            logo = branding.get("logo")
            if logo is not None:
                if not isinstance(logo, dict):
                    raise ValueError("配置档案 branding.logo 必须是对象")
                unknown_logo = sorted(
                    set(logo) - {"filename", "content_type", "data_base64"}
                )
                if unknown_logo:
                    raise ValueError(
                        "配置档案 branding.logo 包含未知参数：{}".format(
                            ", ".join(unknown_logo)
                        )
                    )
                encoded = logo.get("data_base64")
                if not isinstance(encoded, str) or not encoded:
                    raise ValueError("配置档案 branding.logo 缺少 data_base64")
                try:
                    content = base64.b64decode(encoded, validate=True)
                except (ValueError, TypeError, UnicodeError):
                    raise ValueError("配置档案 branding.logo 编码无效")
                validated_logo = validate_logo(
                    logo.get("filename"),
                    logo.get("content_type"),
                    content,
                )
        return normalized_values, validated_logo

    def apply_configuration_profile(self, payload):
        """Apply a versioned JSON deployment profile without storing the profile file."""
        values, validated_logo = self._validate_configuration_profile(payload)
        result = self.update_configuration(values)
        profile_result = {
            "version": 1,
            "changed": result["changed"],
            "values": result["values"],
        }
        if validated_logo is not None:
            asset = self.store.write_branding_asset(**validated_logo)
            self.render_public_site_configuration()
            profile_result["branding"] = {
                "logo": {
                    key: value
                    for key, value in asset.items()
                    if key not in {"content", "name"}
                }
            }
        return profile_result

    def import_configuration_profile_once(self, payload, preserve_existing=False):
        canonical = json.dumps(
            payload,
            ensure_ascii=False,
            sort_keys=True,
            separators=(",", ":"),
        )
        digest = hashlib.sha256(canonical.encode("utf-8")).hexdigest()
        imported_digest = self.store.metadata_value(PROFILE_DIGEST_METADATA_KEY)
        if imported_digest:
            if imported_digest != digest:
                raise ValueError(
                    "部署配置档案已经导入；后续修改请使用配置中心，拒绝以新档案覆盖数据库"
                )
            return {
                "version": 1,
                "imported": False,
                "sha256": digest,
                "values": self.configuration()["values"],
            }
        if preserve_existing:
            # Existing production environments already use SQLite as the source
            # of truth. Validate and register the one-time bootstrap profile, but
            # never replay it over configuration-center changes made after deployment.
            self._validate_configuration_profile(payload)
            result = {
                "version": 1,
                "changed": [],
                "values": self.configuration()["values"],
                "preserved_existing": True,
            }
        else:
            result = self.apply_configuration_profile(payload)
        self.store.write_metadata(PROFILE_DIGEST_METADATA_KEY, digest)
        return {
            **result,
            "imported": True,
            "sha256": digest,
        }

    def verify_control_plane_store(self):
        result = self.store.verify()
        result["secret_store"] = {
            "encrypted": result.pop("encrypted", []),
            "decryptable": result.pop("decryptable", []),
            "key_file": str(self.store.encryption_key_path),
        }
        return result

    def cliproxy_image_runtime_state(self):
        payload = self.store.read_runtime_state("cliproxy_image", {})
        return dict(payload) if isinstance(payload, dict) else {}

    def deployment_runtime_state(self):
        """Return normalized pending/applied application deployment state."""
        payload = self.store.read_runtime_state("deployment", {})
        payload = dict(payload) if isinstance(payload, dict) else {}
        if "pending" in payload or "applied" in payload:
            return {
                name: dict(payload.get(name) or {})
                for name in ("pending", "applied")
                if isinstance(payload.get(name), dict) and payload.get(name)
            }
        # Compatibility with the former flat runtime_state payload. The next
        # deployment or legacy migration rewrites it to the explicit shape.
        return {"applied": payload} if payload else {}

    def applied_deployment(self):
        return dict(self.deployment_runtime_state().get("applied") or {})

    def compose_deployment(self):
        state = self.deployment_runtime_state()
        return dict(state.get("pending") or state.get("applied") or {})

    def applied_cliproxy_image_ref(self):
        applied = self.cliproxy_image_runtime_state().get("applied") or {}
        resolved = str(applied.get("resolved_ref") or "").strip()
        return resolved or self.configuration()["values"]["runtime.cliproxy_image"]

    def seed_cliproxy_applied_image(self, image_ref):
        """Record the pre-migration CPA image without replacing newer state."""
        image_ref = str(image_ref or "").strip()
        if not image_ref or self.cliproxy_image_runtime_state().get("applied"):
            return None
        image = self._docker_json("image", "inspect", image_ref)
        if not image:
            raise ValueError("无法读取要保留的 CPA 镜像：{}".format(image_ref))
        source_ref = self.configuration()["values"]["runtime.cliproxy_image"]
        identity = self._resolve_cliproxy_image_identity(source_ref, image=image)
        identity["migration_source"] = "running_container"
        return self._commit_cliproxy_applied(identity)

    def render_compose_environment(self, cliproxy_image=None):
        """Project authoritative SQLite state into the private Compose env file."""
        with self.compose_environment_lock():
            return self._render_compose_environment_unlocked(
                cliproxy_image=cliproxy_image
            )

    def _render_compose_environment_unlocked(self, cliproxy_image=None):
        values = self.configuration()["values"]
        deployment = self.compose_deployment()
        replacements = {
            "CLIPROXY_IMAGE": str(
                cliproxy_image or self.applied_cliproxy_image_ref()
            ).strip(),
            "ADMIN_IMAGE": str(deployment.get("admin_image") or "codex-cpa-admin:local"),
            "WEB_RUNTIME_IMAGE": str(deployment.get("web_image") or "codex-cpa-web:local"),
            "GATEWAY_RUNTIME_IMAGE": str(deployment.get("gateway_image") or "codex-cpa-gateway:local"),
            "EDGE_RUNTIME_IMAGE": str(deployment.get("edge_image") or "codex-cpa-edge:local"),
            "ADMIN_BASE_IMAGE": str(values["runtime.admin_base_image"]),
            "GATEWAY_IMAGE": str(values["runtime.gateway_image"]),
            "EDGE_IMAGE": str(values["runtime.gateway_image"]),
        }
        for key, env_key in COMPOSE_SETTING_ENV_KEYS.items():
            replacements[env_key] = str(values[key])
        for name, value in replacements.items():
            if "\n" in value or "\r" in value:
                raise ValueError("Compose 环境值包含换行：{}".format(name))
        order = (
            "CLIPROXY_IMAGE",
            "ADMIN_IMAGE",
            "WEB_RUNTIME_IMAGE",
            "GATEWAY_RUNTIME_IMAGE",
            "EDGE_RUNTIME_IMAGE",
            "ADMIN_BASE_IMAGE",
            "GATEWAY_IMAGE",
            "EDGE_IMAGE",
            "GATEWAY_LISTEN_ADDRESS",
            "GATEWAY_PORT",
            "GATEWAY_INTERNAL_PORT",
            "MANAGEMENT_LISTEN_ADDRESS",
            "MANAGEMENT_PORT",
            "BUSINESS_CPA_LISTEN_ADDRESS",
        )
        content = "# Generated from state/control-plane.sqlite3; do not edit.\n"
        content += "\n".join(
            "{}={}".format(name, replacements[name]) for name in order
        ) + "\n"
        self._atomic_text(self.compose_env_path, content, 0o600)
        return dict(replacements)

    def sync_environment_configuration(self, values=None):
        # Compatibility facade for Admin/profile callers. SQLite is already
        # authoritative; only the generated Compose projection is refreshed.
        return self.render_compose_environment()

    def sync_cliproxy_image_environment(self, image_ref=None):
        image_ref = str(
            image_ref
            or self.configuration()["values"]["runtime.cliproxy_image"]
        ).strip()
        if not image_ref:
            raise ValueError("CLIProxyAPI 镜像不能为空")
        return self.render_compose_environment(cliproxy_image=image_ref)

    def _read_account_records(self):
        return self.store.read_accounts()

    def _write_accounts(self, records):
        self.store.write_accounts(records)

    def accounts(self):
        return {
            item["id"]: {
                "email": item["email"],
                "port": int(item["port"]),
                "created_at": int(item.get("created_at", 0)),
                # 兼容旧存储字段，但账号标识是唯一的展示与路由身份。
                "group_name": str(item["id"]),
                "group_enabled": item.get("group_enabled", True) is not False,
                "default_group": item.get("default_group", False) is True,
                "proxy_mode": str(item.get("proxy_mode") or "inherit"),
            }
            for item in self._read_account_records()
        }

    @staticmethod
    def _account_proxy_secret_name(account):
        return "{}{}".format(ACCOUNT_PROXY_SECRET_PREFIX, account)

    @staticmethod
    def _normalize_proxy_mode(value):
        mode = str(value or "inherit").strip().lower()
        if mode not in {"inherit", "custom", "direct"}:
            raise ValueError("CPA 代理模式必须为继承默认、自定义代理或强制直连")
        return mode

    def account_proxy_url(self, account):
        account = self._normalize_account_id(account)
        return self.store.read_secret(self._account_proxy_secret_name(account), "")

    def account_proxy_configuration(self, account, metadata=None):
        account = self._normalize_account_id(account)
        metadata = metadata or self.accounts().get(account)
        if not metadata:
            raise ValueError("业务 CPA 不存在：{}".format(account))
        mode = self._normalize_proxy_mode(metadata.get("proxy_mode"))
        custom_url = self.account_proxy_url(account)
        values = self.configuration()["values"]
        if mode == "custom":
            if not custom_url:
                raise ValueError("CPA {} 已选择自定义代理，但没有配置代理 URL".format(account))
            effective_url = custom_url
            source = "account"
        elif mode == "direct":
            effective_url = "direct"
            source = "direct"
        elif values["cpa.proxy_enabled"]:
            effective_url = values["cpa.proxy_url"]
            source = "default"
        else:
            effective_url = "direct"
            source = "direct"
        return {
            "mode": mode,
            "configured": bool(custom_url),
            "effective_url": effective_url,
            "source": source,
        }

    @staticmethod
    def redact_proxy_url(value):
        if not value or value == "direct":
            return value or ""
        parsed = urllib.parse.urlsplit(str(value))
        host = parsed.hostname or ""
        if ":" in host and not host.startswith("["):
            host = "[{}]".format(host)
        auth = ""
        if parsed.username is not None:
            auth = "{}:{}@".format(
                urllib.parse.quote(urllib.parse.unquote(parsed.username), safe=""),
                "***",
            )
        port = ":{}".format(parsed.port) if parsed.port is not None else ""
        return "{}://{}{}{}".format(parsed.scheme, auth, host, port)

    def _read_routes(self):
        return self.store.read_routes()

    def _write_routes(self, routes):
        self.store.write_routes(routes)

    def default_group(self, accounts=None):
        """Compatibility fallback for legacy callers; new users receive explicit routes."""
        accounts = accounts or self.accounts()
        enabled = [account for account, metadata in accounts.items() if metadata["group_enabled"]]
        if not enabled:
            raise ValueError("当前没有可用的 CPA 账号")
        return next(
            (account for account in enabled if accounts[account]["default_group"]),
            enabled[0],
        )

    def user_route(self, user, accounts=None):
        route = self.explicit_user_route(user, accounts=accounts)
        if route:
            return route
        return self.default_group(accounts)

    def explicit_user_route(self, user, accounts=None):
        """Return only a valid route deliberately stored for the user."""
        user = self._normalize_user(user)
        return self.explicit_user_routes([user], accounts=accounts)[user]

    def explicit_user_routes(self, users, accounts=None):
        """Resolve stored routes for multiple users with one state-file read."""
        accounts = accounts or self.accounts()
        stored = self._read_routes()
        result = {}
        for raw_user in users:
            user = self._normalize_user(raw_user)
            route = stored.get(user)
            result[user] = (
                route
                if route in accounts and accounts[route]["group_enabled"]
                else None
            )
        return result

    def routed_user_counts(self, accounts=None):
        """Count persisted user routes without inventing a default account."""
        accounts = accounts or self.accounts()
        counts = {account: 0 for account in accounts}
        for route in self._read_routes().values():
            if route in counts:
                counts[route] += 1
        return counts

    def groups(self):
        return [
            {
                "id": account,
                "name": metadata["group_name"],
                "account": account,
                "account_email": metadata["email"],
                "enabled": metadata["group_enabled"],
                "default": account == self.default_group(),
            }
            for account, metadata in self.accounts().items()
        ]

    def services(self):
        return {account: "cliproxy-{}".format(account) for account in self.accounts()}

    def instance_name(self):
        environment = read_env(self.root / ".env")
        name = str(
            os.environ.get("INSTANCE_NAME")
            or environment.get("INSTANCE_NAME")
            or "cliproxy"
        ).strip().lower()
        if not re.fullmatch(r"[a-z][a-z0-9-]{1,31}", name):
            raise ValueError("INSTANCE_NAME 必须为 2-32 位小写字母、数字或连字符")
        return name

    def account_container_name(self, account):
        return "{}-{}".format(self.instance_name(), self._normalize_account_id(account))

    def rollback_image_ref(self, account):
        return "{}-cpa-rollback:{}".format(
            self.instance_name(),
            self._normalize_account_id(account),
        )

    def runtime_services_for_account(self, account):
        return [self.services()[account]]

    def runtime_services(self):
        return list(self.services().values())

    def _apply_account_proxy_change(self, account):
        self.render()
        self.compose("config", "--quiet")
        self.compose(
            "up",
            "-d",
            "--no-deps",
            "--force-recreate",
            self.services()[account],
        )
        self._reload_gateway_if_running()

    def apply_default_proxy_change(self):
        """Apply a default proxy change only to accounts that inherit it."""
        self.render()
        self.compose("config", "--quiet")
        inherited = [
            self.services()[account]
            for account, metadata in self.accounts().items()
            if self._normalize_proxy_mode(metadata.get("proxy_mode")) == "inherit"
        ]
        if inherited:
            self.compose(
                "up", "-d", "--no-deps", "--force-recreate", *inherited
            )
        self._reload_gateway_if_running()

    @staticmethod
    def _normalize_account_id(value):
        account = str(value or "").strip().lower()
        if not ACCOUNT_ID_RE.fullmatch(account):
            raise ValueError("账号标识需为 2-32 位小写字母、数字或连字符，并以字母开头")
        if account in RESERVED_ACCOUNT_IDS:
            raise ValueError("账号标识为系统保留名称：{}".format(account))
        return account

    @staticmethod
    def _normalize_account_email(value):
        email = str(value or "").strip().lower()
        if len(email) > 254 or not ACCOUNT_EMAIL_RE.fullmatch(email):
            raise ValueError("上游账号邮箱格式无效")
        return email

    def add_account(
        self,
        account_id,
        email,
        group_name=None,
        apply=True,
        proxy_mode="inherit",
        proxy_url=None,
    ):
        account_id = self._normalize_account_id(account_id)
        email = self._normalize_account_email(email)
        group_name = account_id
        proxy_mode = self._normalize_proxy_mode(proxy_mode)
        normalized_proxy_url = self._normalize_configuration_value(
            CONFIG_DEFINITION_BY_KEY["cpa.proxy_url"], proxy_url or ""
        )
        if proxy_mode == "custom" and not normalized_proxy_url:
            raise ValueError("选择自定义代理时必须配置 CPA 代理 URL")
        original_accounts = self._read_account_records()
        if any(item["id"] == account_id for item in original_accounts):
            raise ValueError("业务 CPA 已存在：{}".format(account_id))
        if any(str(item["email"]).lower() == email for item in original_accounts):
            raise ValueError("上游账号邮箱已存在：{}".format(email))
        configuration = self.configuration()["values"]
        used_ports = {int(item["port"]) for item in original_accounts}
        used_ports.update({8317, 8318, configuration["gateway.port"]})
        port = next(
            (
                value
                for value in range(
                    configuration["accounts.port_start"],
                    configuration["accounts.port_end"] + 1,
                )
                if value not in used_ports
            ),
            None,
        )
        if port is None:
            raise ValueError("没有可用的业务 CPA 端口")
        record = {
            "id": account_id,
            "email": email,
            "port": port,
            "created_at": int(time.time()),
            "group_name": group_name,
            "group_enabled": True,
            "default_group": not original_accounts,
            "proxy_mode": proxy_mode,
        }
        updated_accounts = original_accounts + [record]
        original_keys = [dict(item) for item in self._read_registry()]
        existing_users = sorted(
            {item["user"] for item in original_keys if item["status"] == "active"}
        )
        issued_snapshot = (
            self.issued_path.read_text(encoding="utf-8") if self.issued_path.exists() else None
        )
        service = "cliproxy-{}".format(account_id)
        runtime_services = [service]
        created_keys = []
        try:
            self._write_accounts(updated_accounts)
            if normalized_proxy_url:
                self.store.write_secret(
                    self._account_proxy_secret_name(account_id), normalized_proxy_url
                )
            (self.root / "auth" / account_id).mkdir(parents=True, exist_ok=True)
            (self.root / "logs" / account_id).mkdir(parents=True, exist_ok=True)
            created_keys = [
                self._new_record(
                    "{}:{}".format(user, account_id),
                    key=self.user_active_key(user, records=original_keys),
                )
                for user in existing_users
            ]
            self._write_registry(original_keys + created_keys)
            if created_keys:
                self._append_issued(created_keys)
            if apply:
                self.render()
                self.compose("config", "--quiet")
                self.compose("up", "-d", *runtime_services)
                self._reload_gateway_if_running()
        except Exception:
            if apply:
                try:
                    self.compose("rm", "-s", "-f", *runtime_services, check=False)
                except Exception:
                    pass
            try:
                self._write_accounts(original_accounts)
                self.store.delete_secret(self._account_proxy_secret_name(account_id))
                self._write_registry(original_keys)
                if issued_snapshot is None:
                    if self.issued_path.exists():
                        self.issued_path.unlink()
                else:
                    self._atomic_text(self.issued_path, issued_snapshot, 0o600)
                for path in (
                    self.root / "configs" / "{}.yaml".format(account_id),
                    self.root / "auth" / account_id,
                    self.root / "logs" / account_id,
                ):
                    if path.is_dir() and not path.is_symlink():
                        shutil.rmtree(path)
                    elif path.exists() or path.is_symlink():
                        path.unlink()
                if apply:
                    self.render()
                    self._reload_gateway_if_running()
            except Exception:
                pass
            raise
        result = dict(record)
        result["created_keys"] = len(created_keys)
        return result

    def update_account_policy(
        self,
        account_id,
        group_name,
        group_enabled,
        default_group=False,
        fallback_account=None,
        apply=True,
    ):
        account_id = self._normalize_account_id(account_id)
        group_name = account_id
        if not isinstance(group_enabled, bool) or not isinstance(default_group, bool):
            raise ValueError("账号启用状态和默认状态必须为布尔值")

        original_accounts = [dict(item) for item in self._read_account_records()]
        if not any(item["id"] == account_id for item in original_accounts):
            raise ValueError("业务 CPA 不存在：{}".format(account_id))
        was_default = account_id == self.default_group(self.accounts())
        updated_accounts = [dict(item) for item in original_accounts]
        target = next(item for item in updated_accounts if item["id"] == account_id)
        original_routes = self._read_routes()
        updated_routes = dict(original_routes)
        rerouted_users = 0

        target["group_name"] = group_name
        target["group_enabled"] = group_enabled
        if not group_enabled:
            enabled_others = [
                item for item in updated_accounts
                if item["id"] != account_id and item.get("group_enabled", True) is not False
            ]
            if not enabled_others:
                raise ValueError("至少保留一个可选 CPA，不能停用最后一个账号")
            affected = sorted(user for user, route in original_routes.items() if route == account_id)
            fallback = None
            if fallback_account:
                fallback_id = self._normalize_account_id(fallback_account)
                fallback = next(
                    (item for item in enabled_others if item["id"] == fallback_id),
                    None,
                )
                if not fallback:
                    raise ValueError("备用 CPA 必须是其他已启用账号")
            elif affected:
                raise ValueError("该 CPA 仍有用户正在使用，请选择备用 CPA")
            else:
                fallback = next(
                    (item for item in enabled_others if item.get("default_group") is True),
                    enabled_others[0],
                )
            if affected:
                for user in affected:
                    updated_routes[user] = fallback["id"]
                rerouted_users = len(affected)
            if was_default:
                for item in updated_accounts:
                    item["default_group"] = item["id"] == fallback["id"]
            target["default_group"] = False

        if default_group:
            if not group_enabled:
                raise ValueError("停用账号不能设为默认 CPA")
            for item in updated_accounts:
                item["default_group"] = item["id"] == account_id

        self._write_accounts(updated_accounts)
        self._write_routes(updated_routes)
        try:
            self.render()
            if apply:
                self.compose("config", "--quiet")
                self._reload_gateway_if_running()
        except Exception:
            self._write_accounts(original_accounts)
            self._write_routes(original_routes)
            try:
                self.render()
                if apply:
                    self._reload_gateway_if_running()
            except Exception:
                pass
            raise
        metadata = self.accounts()[account_id]
        return {
            "id": account_id,
            "group_name": metadata["group_name"],
            "group_enabled": metadata["group_enabled"],
            "default_group": metadata["default_group"],
            "rerouted_users": rerouted_users,
        }

    def update_account(
        self,
        account_id,
        email,
        new_account_id=None,
        group_name=None,
        group_enabled=None,
        default_group=None,
        fallback_account=None,
        apply=True,
        proxy_mode=UNSET,
        proxy_url=UNSET,
    ):
        account_id = self._normalize_account_id(account_id)
        new_account_id = self._normalize_account_id(new_account_id or account_id)
        email = self._normalize_account_email(email)
        if group_enabled is not None and not isinstance(group_enabled, bool):
            raise ValueError("账号启用状态必须为布尔值")
        if default_group is not None and not isinstance(default_group, bool):
            raise ValueError("默认状态必须为布尔值")
        original_accounts = [dict(item) for item in self._read_account_records()]
        if not any(item["id"] == account_id for item in original_accounts):
            raise ValueError("业务 CPA 不存在：{}".format(account_id))
        if new_account_id != account_id and any(
            item["id"] == new_account_id for item in original_accounts
        ):
            raise ValueError("业务 CPA 已存在：{}".format(new_account_id))
        if any(item["id"] != account_id and str(item["email"]).lower() == email for item in original_accounts):
            raise ValueError("上游账号邮箱已存在：{}".format(email))

        updated_accounts = [dict(item) for item in original_accounts]
        account_record = next(item for item in updated_accounts if item["id"] == account_id)
        original_proxy_mode = self._normalize_proxy_mode(
            account_record.get("proxy_mode", "inherit")
        )
        original_proxy_url = self.account_proxy_url(account_id)
        desired_proxy_mode = (
            original_proxy_mode
            if proxy_mode is UNSET
            else self._normalize_proxy_mode(proxy_mode)
        )
        desired_proxy_url = original_proxy_url
        if proxy_url is not UNSET and proxy_url is not None and str(proxy_url).strip():
            desired_proxy_url = self._normalize_configuration_value(
                CONFIG_DEFINITION_BY_KEY["cpa.proxy_url"], proxy_url
            )
        if desired_proxy_mode == "custom" and not desired_proxy_url:
            raise ValueError("选择自定义代理时必须配置 CPA 代理 URL")
        account_record["proxy_mode"] = desired_proxy_mode
        proxy_changed = (
            desired_proxy_mode != original_proxy_mode
            or desired_proxy_url != original_proxy_url
        )
        current_metadata = self.accounts()
        was_default = account_id == self.default_group(current_metadata)
        account_record["id"] = new_account_id
        account_record["email"] = email
        account_record["group_name"] = new_account_id
        desired_enabled = (
            account_record.get("group_enabled", True) is not False
            if group_enabled is None
            else group_enabled
        )
        if not desired_enabled and default_group is True:
            raise ValueError("停用账号不能设为默认 CPA")
        account_record["group_enabled"] = desired_enabled
        original_keys = [dict(item) for item in self._read_registry()]
        updated_keys = [dict(item) for item in original_keys]
        for item in updated_keys:
            if item["account"] == account_id:
                item["account"] = new_account_id
                item["label"] = "{}:{}".format(item["user"], new_account_id)
                item["account_email"] = email

        original_routes = self._read_routes()
        updated_routes = dict(original_routes)
        rerouted_users = 0
        if not desired_enabled:
            enabled_others = [
                item
                for item in updated_accounts
                if item["id"] != new_account_id
                and item.get("group_enabled", True) is not False
            ]
            if not enabled_others:
                raise ValueError("至少保留一个可选 CPA，不能停用最后一个账号")
            affected = sorted(
                user for user, route in original_routes.items() if route == account_id
            )
            fallback = None
            if fallback_account:
                fallback_id = self._normalize_account_id(fallback_account)
                fallback = next(
                    (item for item in enabled_others if item["id"] == fallback_id),
                    None,
                )
                if not fallback:
                    raise ValueError("备用 CPA 必须是其他已启用账号")
            elif affected:
                raise ValueError("该 CPA 仍有用户正在使用，请选择备用 CPA")
            else:
                fallback = next(
                    (
                        item
                        for item in enabled_others
                        if item.get("default_group") is True
                    ),
                    enabled_others[0],
                )
            for user in affected:
                updated_routes[user] = fallback["id"]
            rerouted_users = len(affected)
            if was_default:
                for item in updated_accounts:
                    item["default_group"] = item["id"] == fallback["id"]
            account_record["default_group"] = False
        else:
            updated_routes = {
                user: (new_account_id if route == account_id else route)
                for user, route in original_routes.items()
            }
            if default_group is True:
                for item in updated_accounts:
                    item["default_group"] = item["id"] == new_account_id

        renamed = new_account_id != account_id
        backup = None
        path_moves = []
        old_service = "cliproxy-{}".format(account_id)
        new_service = "cliproxy-{}".format(new_account_id)
        old_runtime_services = self.runtime_services_for_account(account_id)
        new_runtime_services = [new_service]
        old_config = self.root / "configs" / "{}.yaml".format(account_id)
        new_config = self.root / "configs" / "{}.yaml".format(new_account_id)
        if renamed:
            for relative in ("auth", "logs"):
                source = self.root / relative / account_id
                destination = self.root / relative / new_account_id
                if destination.exists() or destination.is_symlink():
                    raise ValueError("重命名目标已存在：{}".format(destination.relative_to(self.root)))
                if source.exists() or source.is_symlink():
                    path_moves.append((source, destination))
            if new_config.exists() or new_config.is_symlink():
                raise ValueError("重命名目标已存在：{}".format(new_config.relative_to(self.root)))
            backup = self._create_account_backup(
                account_id,
                "renamed-to-{}".format(new_account_id),
                account_record=next(
                    dict(item) for item in original_accounts if item["id"] == account_id
                ),
                key_records=[item for item in original_keys if item["account"] == account_id],
            )

        old_service_removed = False
        try:
            if renamed and apply:
                self.compose("rm", "-s", "-f", *old_runtime_services)
                old_service_removed = True
            for source, destination in path_moves:
                source.rename(destination)
            self._write_accounts(updated_accounts)
            target_proxy_secret = self._account_proxy_secret_name(new_account_id)
            if desired_proxy_url:
                self.store.write_secret(target_proxy_secret, desired_proxy_url)
            else:
                self.store.delete_secret(target_proxy_secret)
            if renamed:
                self.store.delete_secret(self._account_proxy_secret_name(account_id))
            self._write_registry(updated_keys)
            self._write_routes(updated_routes)
            if renamed:
                self.render()
                if apply:
                    self.compose("config", "--quiet")
                    self.compose("up", "-d", *new_runtime_services)
                    self._reload_gateway_if_running()
                if old_config.exists() or old_config.is_symlink():
                    old_config.unlink()
            elif apply:
                if proxy_changed:
                    self._apply_account_proxy_change(new_account_id)
                else:
                    self.apply_changes()
        except Exception:
            try:
                if renamed and apply:
                    self.compose("rm", "-s", "-f", *new_runtime_services, check=False)
                for source, destination in reversed(path_moves):
                    if (destination.exists() or destination.is_symlink()) and not (
                        source.exists() or source.is_symlink()
                    ):
                        destination.rename(source)
                self._write_accounts(original_accounts)
                self.store.delete_secret(self._account_proxy_secret_name(new_account_id))
                if original_proxy_url:
                    self.store.write_secret(
                        self._account_proxy_secret_name(account_id), original_proxy_url
                    )
                self._write_registry(original_keys)
                self._write_routes(original_routes)
                if renamed:
                    if new_config.exists() or new_config.is_symlink():
                        new_config.unlink()
                    self.render()
                    if apply and old_service_removed:
                        self.compose("up", "-d", *old_runtime_services)
                    if apply:
                        self._reload_gateway_if_running()
                elif apply:
                    if proxy_changed:
                        self._apply_account_proxy_change(account_id)
                    else:
                        self.apply_changes()
            except Exception:
                pass
            raise
        result = dict(account_record)
        result["rerouted_users"] = rerouted_users
        if renamed:
            result["renamed_from"] = account_id
            result["backup"] = str(backup.relative_to(self.root))
        return result

    def _create_account_backup(
        self,
        account_id,
        reason,
        account_record=None,
        key_records=None,
        include_config=True,
        include_auth=True,
        include_logs=True,
    ):
        account_id = self._normalize_account_id(account_id)
        account_record = account_record or next(
            (dict(item) for item in self._read_account_records() if item["id"] == account_id),
            None,
        )
        if not account_record:
            raise ValueError("业务 CPA 不存在：{}".format(account_id))
        backup_root = self.root / "backups" / "accounts"
        backup_root.mkdir(parents=True, exist_ok=True)
        os.chmod(backup_root, 0o700)
        backup = backup_root / "{}-{}-{}-{}".format(
            time.strftime("%Y%m%d-%H%M%S"), account_id, reason, secrets.token_hex(3)
        )
        backup.mkdir(mode=0o700)
        self._atomic_text(
            backup / "account.json",
            json.dumps(account_record, ensure_ascii=False, indent=2) + "\n",
            0o600,
        )
        if key_records is None:
            key_records = [item for item in self._read_registry() if item["account"] == account_id]
        self._atomic_text(
            backup / "keys.json",
            json.dumps(key_records, ensure_ascii=False, indent=2) + "\n",
            0o600,
        )
        config = self.root / "configs" / "{}.yaml".format(account_id)
        if include_config and config.is_file():
            shutil.copy2(config, backup / "config.yaml")
            os.chmod(backup / "config.yaml", 0o600)
        for name, source in (
            ("auth", self.root / "auth" / account_id),
            ("logs", self.root / "logs" / account_id),
        ):
            if source.is_dir() and ((name == "auth" and include_auth) or (name == "logs" and include_logs)):
                shutil.copytree(source, backup / name)
        return backup

    @staticmethod
    def _clear_directory(path):
        path.mkdir(parents=True, exist_ok=True)
        for child in path.iterdir():
            if child.is_dir() and not child.is_symlink():
                shutil.rmtree(child)
            else:
                child.unlink()

    def _restore_directory(self, source, target):
        self._clear_directory(target)
        if not source.is_dir():
            return
        for child in source.iterdir():
            destination = target / child.name
            if child.is_dir() and not child.is_symlink():
                shutil.copytree(child, destination)
            else:
                shutil.copy2(child, destination)

    def clear_account_auth(self, account_id, apply=True):
        account_id = self._normalize_account_id(account_id)
        if account_id not in self.accounts():
            raise ValueError("业务 CPA 不存在：{}".format(account_id))
        backup = self._create_account_backup(
            account_id,
            "oauth-clear",
            key_records=[],
            include_config=False,
            include_logs=False,
        )
        auth_dir = self.root / "auth" / account_id
        service = self.services()[account_id]
        try:
            self._clear_directory(auth_dir)
            if apply:
                self.compose("restart", service)
        except Exception:
            try:
                self._restore_directory(backup / "auth", auth_dir)
                if apply:
                    self.compose("restart", service, check=False)
            except Exception:
                pass
            raise
        return {"id": account_id, "backup": str(backup.relative_to(self.root))}

    def delete_account(
        self,
        account_id,
        revoke_keys=False,
        fallback_account=None,
        apply=True,
    ):
        account_id = self._normalize_account_id(account_id)
        original_accounts = [dict(item) for item in self._read_account_records()]
        account_record = next((item for item in original_accounts if item["id"] == account_id), None)
        if not account_record:
            raise ValueError("业务 CPA 不存在：{}".format(account_id))
        if len(original_accounts) <= 1:
            raise ValueError("至少保留一个业务 CPA，不能删除最后一个账号")
        was_default = account_id == self.default_group(self.accounts())

        original_keys = [dict(item) for item in self._read_registry()]
        account_keys = [item for item in original_keys if item["account"] == account_id]
        active_keys = [item for item in account_keys if item["status"] == "active"]
        active_elsewhere = {
            (item["user"], item["key"])
            for item in original_keys
            if item["status"] == "active" and item["account"] != account_id
        }
        exclusive_keys = [
            item for item in active_keys
            if (item["user"], item["key"]) not in active_elsewhere
        ]
        if exclusive_keys and not revoke_keys:
            raise ValueError(
                "该 CPA 仍有 {} 个独占有效 Key，请确认同时停用后再删除".format(
                    len({item["key"] for item in exclusive_keys})
                )
            )

        backup = self._create_account_backup(
            account_id, "deleted", account_record=dict(account_record), key_records=account_keys
        )
        updated_accounts = [item for item in original_accounts if item["id"] != account_id]
        updated_keys = [item for item in original_keys if item["account"] != account_id]
        original_routes = self._read_routes()
        enabled_remaining = [
            item for item in updated_accounts if item.get("group_enabled", True) is not False
        ]
        if fallback_account:
            fallback_id = self._normalize_account_id(fallback_account)
            fallback = next(
                (item for item in enabled_remaining if item["id"] == fallback_id),
                None,
            )
            if not fallback:
                raise ValueError("备用 CPA 必须是其他已启用账号")
            replacement = fallback["id"]
        else:
            replacement = next(
                (
                    item["id"]
                    for item in enabled_remaining
                    if item.get("default_group") is True
                ),
                enabled_remaining[0]["id"]
                if enabled_remaining
                else updated_accounts[0]["id"],
            )
        if was_default:
            for item in updated_accounts:
                item["default_group"] = item["id"] == replacement
        rerouted_users = sum(route == account_id for route in original_routes.values())
        updated_routes = {
            user: (replacement if route == account_id else route)
            for user, route in original_routes.items()
        }
        service = "cliproxy-{}".format(account_id)
        runtime_services = self.runtime_services_for_account(account_id)
        service_removed = False
        try:
            if apply:
                self.compose("rm", "-s", "-f", *runtime_services)
                service_removed = True
            self._write_accounts(updated_accounts)
            self._write_registry(updated_keys)
            self._write_routes(updated_routes)
            self.render()
            if apply:
                self.compose("config", "--quiet")
                self._reload_gateway_if_running()
        except Exception:
            try:
                self._write_accounts(original_accounts)
                self._write_registry(original_keys)
                self._write_routes(original_routes)
                self.render()
                if apply and service_removed:
                    self.compose("up", "-d", *runtime_services)
                if apply:
                    self._reload_gateway_if_running()
            except Exception:
                pass
            raise

        cleanup_warnings = []
        self.store.delete_secret(self._account_proxy_secret_name(account_id))
        for path in (
            self.root / "configs" / "{}.yaml".format(account_id),
            self.root / "auth" / account_id,
            self.root / "logs" / account_id,
        ):
            try:
                if path.is_dir() and not path.is_symlink():
                    shutil.rmtree(path)
                elif path.exists() or path.is_symlink():
                    path.unlink()
            except OSError as error:
                cleanup_warnings.append("{}: {}".format(path.relative_to(self.root), error))
        return {
            "id": account_id,
            "removed_key_records": len(account_keys),
            "replacement_account": replacement,
            "rerouted_users": rerouted_users,
            "backup": str(backup.relative_to(self.root)),
            "cleanup_warnings": cleanup_warnings,
        }


    def _read_registry(self):
        return self.store.read_key_records()

    def _write_registry(self, records):
        self.store.write_key_records(records)

    def _read_internal_keys(self):
        users = self.store.read_internal_keys()
        normalized = {}
        seen = set()
        for raw_user, raw_record in users.items():
            user = self._normalize_user(str(raw_user))
            if not isinstance(raw_record, dict):
                raise ValueError("用户内部 Key 记录无效：{}".format(user))
            key = str(raw_record.get("key", "")).strip()
            if not re.fullmatch(r"cpa_internal_[0-9a-f]{64}", key) or key in seen:
                raise ValueError("用户内部 Key 无效或重复：{}".format(user))
            seen.add(key)
            normalized[user] = {
                "key": key,
                "created_at": int(raw_record.get("created_at", 0)),
                "status": str(raw_record.get("status", "active")),
            }
        return normalized

    def _write_internal_keys(self, users):
        self.store.write_internal_keys(users)

    @staticmethod
    def _new_internal_key():
        return "cpa_internal_{}".format(secrets.token_hex(32))

    def sync_internal_keys(self, active_users=None):
        """Ensure every active user has one stable internal CPA credential."""
        if active_users is None:
            active_users = {
                item["user"] for item in self._read_registry() if item.get("status") == "active"
            }
        active_users = {self._normalize_user(str(item)) for item in active_users}
        users = self._read_internal_keys()
        changed = False
        existing_keys = {item["key"] for item in users.values()}
        now = int(time.time())
        for user in sorted(active_users):
            record = users.get(user)
            if record:
                if record.get("status") != "active":
                    record["status"] = "active"
                    changed = True
                continue
            key = self._new_internal_key()
            while key in existing_keys:
                key = self._new_internal_key()
            existing_keys.add(key)
            users[user] = {"key": key, "created_at": now, "status": "active"}
            changed = True
        for user, record in users.items():
            expected = "active" if user in active_users else "inactive"
            if record.get("status") != expected:
                record["status"] = expected
                changed = True
        if changed:
            self._write_internal_keys(users)
        return {user: dict(users[user]) for user in sorted(active_users)}

    def internal_identity_records(self):
        """Return collector identity rows without exposing them through an API."""
        users = self.sync_internal_keys()
        return [
            {
                "key": record["key"],
                "label": user,
                "user": user,
                "account": "",
            }
            for user, record in users.items()
        ]

    @staticmethod
    def _key_digest(value):
        return hashlib.sha256(str(value).encode("utf-8")).hexdigest()

    def _snapshot_records(self, accounts=None, active=None):
        accounts = accounts or self.accounts()
        active = active if active is not None else self.active_records()
        routes = self._read_routes()
        internal = self.sync_internal_keys({item["user"] for item in active})
        by_user = {}
        for item in active:
            by_user.setdefault(item["user"], []).append(item)
        records = []
        emitted = set()
        for user in sorted(by_user):
            items = by_user[user]
            keys = {item["key"] for item in items}
            if len(keys) == 1:
                route = routes.get(user)
                if route not in accounts or not accounts[route]["group_enabled"]:
                    continue
                candidates = [item for item in items if item["account"] == route]
                if not candidates:
                    continue
                pairs = [(next(iter(keys)), route)]
            else:
                # Rollback/migration compatibility for legacy per-CPA external keys.
                pairs = [(item["key"], item["account"]) for item in items]
            for external_key, account in pairs:
                digest = self._key_digest(external_key)
                if digest in emitted or account not in accounts:
                    continue
                emitted.add(digest)
                records.append(
                    {
                        "external_key_sha256": digest,
                        "user_email": user,
                        "account": account,
                        "backend": "cliproxy-{}:8317".format(account),
                        "internal_key": internal[user]["key"],
                        "label": "{}:{}".format(user, account),
                    }
                )
        return records

    def publish_auth_snapshot(self, wait=False, timeout=8):
        records = self._snapshot_records()
        generation = uuid.uuid4().hex
        payload = {
            "version": GATEWAY_SNAPSHOT_VERSION,
            "generation": generation,
            "generated_at": int(time.time()),
            "records": records,
        }
        self._atomic_text(
            self.auth_snapshot_path,
            json.dumps(payload, ensure_ascii=False, separators=(",", ":")) + "\n",
            0o640,
        )
        self._grant_gateway_snapshot_access(self.auth_snapshot_path)
        if wait:
            self.wait_for_gateway_snapshot("auth", generation, timeout=timeout)
        return {"generation": generation, "records": len(records)}

    def publish_quota_snapshot(self, quotas, generated_at=None, wait=False, timeout=8):
        generated_at = int(time.time()) if generated_at is None else int(generated_at)
        records = []
        for user in sorted(quotas):
            quota = quotas[user]
            records.append(
                {
                    "user_email": user,
                    "week_start_at": int(quota["week_start_at"]),
                    "week_end_at": int(quota["week_end_at"]),
                    "limit_tokens": -1 if quota["limit_tokens"] is None else int(quota["limit_tokens"]),
                    "used_tokens": int(quota["used_tokens"]),
                    "raw_used_tokens": int(quota.get("raw_used_tokens", quota["used_tokens"])),
                    "weighted_raw_used_tokens": int(
                        quota.get("weighted_raw_used_tokens", quota["used_tokens"])
                    ),
                    "quota_unit": "weighted_tokens",
                }
            )
        semantic = json.dumps(records, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        digest = hashlib.sha256(semantic.encode("utf-8")).hexdigest()
        previous = None
        try:
            previous = json.loads(self.quota_snapshot_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            pass
        if previous and previous.get("content_sha256") == digest:
            self._grant_gateway_snapshot_access(self.quota_snapshot_path)
            return {
                "generation": previous.get("generation", ""),
                "records": len(records),
                "changed": False,
            }
        generation = uuid.uuid4().hex
        payload = {
            "version": GATEWAY_SNAPSHOT_VERSION,
            "generation": generation,
            "generated_at": generated_at,
            "content_sha256": digest,
            "records": records,
        }
        self._atomic_text(
            self.quota_snapshot_path,
            json.dumps(payload, ensure_ascii=False, separators=(",", ":")) + "\n",
            0o640,
        )
        self._grant_gateway_snapshot_access(self.quota_snapshot_path)
        if wait:
            self.wait_for_gateway_snapshot("quota", generation, timeout=timeout)
        return {"generation": generation, "records": len(records), "changed": True}

    def publish_quota_heartbeat(
        self,
        ok=True,
        error="",
        now=None,
        stale_after_seconds=15,
        fail_open_after_seconds=300,
    ):
        now = int(time.time()) if now is None else int(now)
        previous = {}
        try:
            previous = json.loads(self.quota_heartbeat_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            pass
        last_success_at = now if ok else int(previous.get("last_success_at") or 0)
        payload = {
            "version": GATEWAY_SNAPSHOT_VERSION,
            "updated_at": now,
            "ok": bool(ok),
            "error": str(error or "")[:500],
            "stale_after_seconds": max(5, int(stale_after_seconds)),
            "last_success_at": last_success_at,
            "fail_open_after_seconds": max(30, int(fail_open_after_seconds)),
        }
        self._atomic_text(
            self.quota_heartbeat_path,
            json.dumps(payload, ensure_ascii=False, separators=(",", ":")) + "\n",
            0o640,
        )
        self._grant_gateway_snapshot_access(self.quota_heartbeat_path)
        return payload

    @staticmethod
    def _grant_gateway_snapshot_access(path):
        """Allow only root and the OpenResty worker group to read a snapshot."""
        os.chmod(path, 0o640)
        if os.geteuid() == 0:
            os.chown(path, -1, GATEWAY_SNAPSHOT_GID)

    def gateway_snapshot_status(self):
        last_error = None
        for gateway_url in self.gateway_probe_urls():
            request = urllib.request.Request(
                gateway_url + "/__internal/snapshots",
                headers={"Accept": "application/json"},
            )
            try:
                with urllib.request.urlopen(request, timeout=2) as response:
                    return json.load(response)
            except Exception as error:
                last_error = error
        if last_error:
            raise last_error
        raise RuntimeError("网关内部探针地址不可用")

    def wait_for_gateway_snapshot(self, kind, generation, timeout=8):
        deadline = time.monotonic() + float(timeout)
        last_error = None
        while time.monotonic() < deadline:
            try:
                status = self.gateway_snapshot_status()
                if status.get(kind, {}).get("active_generation") == generation:
                    return status[kind]
            except Exception as error:
                last_error = error
            time.sleep(0.1)
        raise RuntimeError(
            "网关未在限定时间内激活 {} 快照{}{}".format(
                kind,
                generation,
                ": {}".format(last_error) if last_error else "",
            )
        )

    def _append_issued(self, records):
        # Full Keys already live in the authoritative control-plane database.
        # Do not create a second append-only plaintext secret store.
        return None

    def _normalize_user(self, user):
        normalized = str(user or "").strip().lower()
        match = USER_EMAIL_RE.fullmatch(normalized)
        domains = self.configuration()["values"]["identity.allowed_email_domains"]
        if not domains:
            raise ValueError("请先在配置中心设置允许的邮箱域名")
        if not match or match.group(1) not in domains:
            raise ValueError(
                "用户标识必须属于允许的邮箱域名：{}".format(
                    ", ".join("@" + domain for domain in domains)
                )
            )
        return normalized

    def _normalize_label(self, label):
        normalized = label.strip().lower()
        if ":" not in normalized:
            raise ValueError("Key 标识格式应为 user@example.com:账号标识")
        user, account = normalized.rsplit(":", 1)
        user = self._normalize_user(user)
        if account not in self.accounts():
            raise ValueError("未知账号：{}".format(account))
        return "{}:{}".format(user, account)

    @staticmethod
    def _key_user_namespace(user):
        """Return a readable, key-safe username derived from the company email."""
        local_part = user.split("@", 1)[0].lower()
        readable = re.sub(r"[^a-z0-9]+", "_", local_part).strip("_")
        return readable or "user"

    def _new_user_key(self, user):
        prefix = self.configuration()["values"]["identity.key_prefix"]
        return "{}{}_{}".format(prefix, self._key_user_namespace(user), uuid.uuid4())

    def _is_current_user_key(self, user, key):
        value = str(key or "")
        return (
            value.rsplit("_", 1)[0].endswith("_" + self._key_user_namespace(user))
            and USER_KEY_UUID_RE.fullmatch(value) is not None
        )

    def _new_record(self, label, key=None):
        now = int(time.time())
        user, account = label.rsplit(":", 1)
        return {
            "label": label,
            "account": account,
            "account_email": self.accounts()[account]["email"],
            "user": user,
            "status": "active",
            "key": key or self._new_user_key(user),
            "created_at": now,
            "updated_at": now,
        }

    def active_records(self):
        return sorted(
            [item for item in self._read_registry() if item["status"] == "active"],
            key=lambda item: item["label"],
        )

    def user_active_key(self, user, records=None):
        user = self._normalize_user(user)
        records = records if records is not None else self._read_registry()
        active = [item for item in records if item["user"] == user and item["status"] == "active"]
        if not active:
            raise ValueError("{} 没有启用中的 Key".format(user))
        unique = {item["key"] for item in active}
        if len(unique) == 1:
            return next(iter(unique))
        route = self.user_route(user)
        routed = [item for item in active if item["account"] == route]
        chosen = max(routed or active, key=lambda item: (item["updated_at"], item["created_at"]))
        return chosen["key"]

    def _commit_records(self, original, updated, issued=None, apply=True):
        """Persist a registry mutation and roll active state back if apply fails."""
        self._write_registry(updated)
        if issued:
            self._append_issued(issued)
        if not apply:
            return
        try:
            self.apply_changes()
        except Exception:
            # issued-keys.tsv is an append-only audit trail. A failed key remains
            # recorded there, but restoring the registry makes it unusable.
            self._write_registry(original)
            try:
                self.apply_changes()
            except Exception:
                pass
            raise

    def create_key(self, label, apply=True):
        label = self._normalize_label(label)
        records = self._read_registry()
        user, account = label.rsplit(":", 1)
        if any(item["user"] == user and item["status"] == "active" for item in records):
            raise ValueError("{} 已有启用中的统一 Key".format(user))
        created = self.create_user(user, apply=apply, initial_account=account)
        return next(item for item in created if item["account"] == account)

    def create_user(self, user, apply=True, initial_account=None):
        user = self._normalize_user(user)
        if not self.accounts():
            raise ValueError("请先在账号管理中创建至少一个业务 CPA")
        if initial_account is not None:
            initial_account = self._normalize_account_id(initial_account)
            accounts = self.accounts()
            if initial_account not in accounts:
                raise ValueError("业务 CPA 不存在：{}".format(initial_account))
            if not accounts[initial_account]["group_enabled"]:
                raise ValueError("CPA 账号已停用：{}".format(initial_account))
        labels = ["{}:{}".format(user, account) for account in self.accounts()]
        records = self._read_registry()
        active_labels = {item["label"] for item in records if item["status"] == "active"}
        conflicts = sorted(active_labels.intersection(labels))
        if conflicts:
            raise ValueError("以下 Key 已存在：{}".format(",".join(conflicts)))
        key = self._new_user_key(user)
        created = [self._new_record(label, key=key) for label in labels]
        updated = records + created
        original_routes = self._read_routes()
        updated_routes = dict(original_routes)
        if initial_account is None:
            # 新用户首次进入使用中心时自动分配；同时清理同邮箱旧生命周期留下的路由。
            updated_routes.pop(user, None)
        else:
            # ``key create user:account`` 已显式提供账号，保留该明确选择。
            updated_routes[user] = initial_account
        self._write_routes(updated_routes)
        try:
            self._commit_records(records, updated, issued=[created[0]], apply=apply)
        except Exception:
            self._write_routes(original_routes)
            if apply:
                try:
                    self._render_gateway_key_map()
                    self._reload_gateway_if_running()
                except Exception:
                    pass
            raise
        return created

    def create_users(self, users, apply=True, restart_containers=False, dry_run=False):
        """批量创建用户：去重、跳过已有，默认只渲染配置并热重载网关，不重建业务 CPA。"""
        accounts = self.accounts()
        if not accounts:
            raise ValueError("请先在账号管理中创建至少一个业务 CPA")
        requested = []
        seen = set()
        invalid = []
        duplicates_in_input = []
        for raw in users:
            text = str(raw or "").strip()
            if not text:
                continue
            try:
                user = self._normalize_user(text)
            except ValueError as error:
                invalid.append({"email": text, "reason": str(error)})
                continue
            if user in seen:
                duplicates_in_input.append(user)
                continue
            seen.add(user)
            requested.append(user)

        records = self._read_registry()
        active_users = {
            item["user"] for item in records if item["status"] == "active"
        }
        skipped = [user for user in requested if user in active_users]
        to_create = [user for user in requested if user not in active_users]

        created_users = []
        issued = []
        new_records = []
        existing_keys = {item.get("key") for item in records if item.get("key")}
        for user in to_create:
            key = self._new_user_key(user)
            while key in existing_keys:
                key = self._new_user_key(user)
            existing_keys.add(key)
            user_records = [
                self._new_record("{}:{}".format(user, account), key=key)
                for account in accounts
            ]
            new_records.extend(user_records)
            issued.append(user_records[0])
            created_users.append(
                {
                    "email": user,
                    "key": key,
                    "account": None,
                    "accounts": list(accounts),
                }
            )

        result = {
            "requested": len(requested),
            "created": len(created_users),
            "skipped_existing": skipped,
            "duplicates_in_input": duplicates_in_input,
            "invalid": invalid,
            "users": created_users,
            "restart_containers": bool(restart_containers),
            "applied": False,
            "dry_run": bool(dry_run),
        }
        if dry_run or not to_create:
            return result

        original_routes = self._read_routes()
        updated_routes = dict(original_routes)
        for user in to_create:
            updated_routes.pop(user, None)
        updated = records + new_records
        self._write_routes(updated_routes)
        try:
            self._write_registry(updated)
            self._append_issued(issued)
            if apply:
                self.apply_changes(restart_containers=restart_containers)
                result["applied"] = True
        except Exception:
            self._write_registry(records)
            self._write_routes(original_routes)
            try:
                if apply:
                    self.apply_changes(restart_containers=restart_containers)
            except Exception:
                pass
            raise
        return result

    def rotate_key(self, label, apply=True):
        label = self._normalize_label(label)
        records = self._read_registry()
        user, requested_account = label.rsplit(":", 1)
        active = [item for item in records if item["user"] == user and item["status"] == "active"]
        if not active:
            raise ValueError("{} 没有启用中的 Key".format(user))
        now = int(time.time())
        updated = [dict(item) for item in records]
        for item in updated:
            if item["user"] == user and item["status"] == "active":
                item["status"] = "rotated"
                item["updated_at"] = now
        key = self._new_user_key(user)
        replacements = [
            self._new_record("{}:{}".format(user, account), key=key)
            for account in self.accounts()
        ]
        updated.extend(replacements)
        self._write_registry(updated)
        self._append_issued([replacements[0]])
        if not apply:
            return next(
                item for item in replacements if item["account"] == requested_account
            )
        try:
            self.publish_auth_snapshot(wait=False)
        except Exception:
            self._write_registry(records)
            try:
                self.publish_auth_snapshot(wait=False)
            except Exception:
                pass
            raise
        return next(item for item in replacements if item["account"] == requested_account)

    def rotate_legacy_keys(self, apply=True, dry_run=False):
        """Replace every active legacy user key in one atomic deployment."""
        records = self._read_registry()
        accounts = self.accounts()
        users = sorted({item["user"] for item in records if item["status"] == "active"})
        updated = [dict(item) for item in records]
        issued = []
        rotated_users = []
        revoked_keys = set()
        existing_keys = {item.get("key") for item in records if item.get("key")}
        now = int(time.time())

        for user in users:
            active = [
                item for item in updated
                if item["user"] == user and item["status"] == "active"
            ]
            unique = {item["key"] for item in active}
            complete = (
                len(active) == len(accounts)
                and {item["account"] for item in active} == set(accounts)
            )
            if complete and len(unique) == 1 and self._is_current_user_key(user, next(iter(unique))):
                continue

            revoked_keys.update(unique)
            for item in active:
                item["status"] = "rotated"
                item["updated_at"] = now

            key = self._new_user_key(user)
            while key in existing_keys:
                key = self._new_user_key(user)
            existing_keys.add(key)
            replacements = [
                self._new_record("{}:{}".format(user, account), key=key)
                for account in accounts
            ]
            updated.extend(replacements)
            issued.append(replacements[0])
            rotated_users.append(user)

        result = {
            "users": len(users),
            "rotated_users": len(rotated_users),
            "unchanged_users": len(users) - len(rotated_users),
            "revoked_keys": len(revoked_keys),
            "issued_keys": len(issued),
            "dry_run": bool(dry_run),
        }
        if dry_run or not rotated_users:
            return result

        self._commit_records(records, updated, issued=issued, apply=apply)
        return result

    def revoke_key(self, label, apply=True):
        label = self._normalize_label(label)
        user, requested_account = label.rsplit(":", 1)
        matches = self.revoke_user(user, apply=apply)
        return next(
            (item for item in matches if item["account"] == requested_account),
            matches[0],
        )

    def revoke_user(self, user, apply=True):
        user = self._normalize_user(user)
        records = self._read_registry()
        updated = [dict(item) for item in records]
        matches = [item for item in updated if item["user"] == user and item["status"] == "active"]
        if not matches:
            raise ValueError("{} 没有启用中的 Key".format(user))
        now = int(time.time())
        for item in matches:
            item["status"] = "revoked"
            item["updated_at"] = now
        self._commit_records(records, updated, apply=apply)
        return matches

    def delete_user(self, user, revoke_keys=False, apply=True):
        user = self._normalize_user(user)
        records = self._read_registry()
        matches = [item for item in records if item["user"] == user]
        if not matches:
            raise ValueError("用户不存在：{}".format(user))
        active = [item for item in matches if item["status"] == "active"]
        active_key_count = len({item["key"] for item in active})
        if active and not revoke_keys:
            raise ValueError(
                "用户仍有 {} 个有效 Key，请确认同时停用后再删除".format(active_key_count)
            )
        updated = [item for item in records if item["user"] != user]
        original_routes = self._read_routes()
        updated_routes = dict(original_routes)
        updated_routes.pop(user, None)
        self._write_routes(updated_routes)
        try:
            self._commit_records(records, updated, apply=apply)
        except Exception:
            self._write_routes(original_routes)
            raise
        return {
            "email": user,
            "removed_records": len(matches),
            "revoked_active_keys": active_key_count,
        }

    @staticmethod
    def _normalize_management_key(value):
        key = str(value or "")
        if len(key) < 12 or len(key) > 128:
            raise ValueError("管理密钥长度必须为 12-128 个字符")
        if any(character.isspace() or ord(character) < 32 for character in key):
            raise ValueError("管理密钥不能包含空白或控制字符")
        return key

    def management_key(self):
        key = self.store.read_secret("cpa_management_key")
        if not key:
            raise ValueError("缺少 CPA 管理密钥")
        return self._normalize_management_key(key)

    def rotate_management_key(self, new_key, apply=True):
        new_key = self._normalize_management_key(new_key)
        key_path = self.root / "secrets" / "cpa-management.key"
        key_existed = key_path.exists()
        old_key = self.management_key()
        if secrets.compare_digest(old_key, new_key):
            raise ValueError("新管理密钥不能与当前密钥相同")

        management_config = self.root / "management" / "config" / "config.yaml"
        old_management_config = management_config.read_text(encoding="utf-8") if management_config.exists() else None
        new_management_config = old_management_config
        if old_management_config is not None:
            new_management_config, replacements = re.subn(
                r"(?m)^(\s*secret-key:\s*).*$",
                lambda match: match.group(1) + json.dumps(new_key),
                old_management_config,
                count=1,
            )
            if replacements != 1:
                raise ValueError("management/config/config.yaml 缺少 remote-management.secret-key")

        try:
            self.store.write_secret("cpa_management_key", new_key)
            if new_management_config is not None:
                self._atomic_text(management_config, new_management_config, 0o600)
            self.render()
            if apply:
                self.compose("config", "--quiet")
                self.compose("restart", "management", *self.services().values())
                self._reload_gateway_if_running()
        except Exception:
            try:
                self.store.write_secret("cpa_management_key", old_key)
                if old_management_config is not None:
                    self._atomic_text(management_config, old_management_config, 0o600)
                self.render()
                if apply:
                    self.compose("restart", "management", *self.services().values(), check=False)
                    self._reload_gateway_if_running()
            except Exception:
                pass
            raise
        if key_existed:
            try:
                key_path.unlink()
            except OSError:
                self._atomic_text(key_path, new_key + "\n", 0o600)
        return {"rotated": True, "services": len(self.services()) + 1}

    def set_user_route(self, user, account, apply=True):
        user = self._normalize_user(user)
        account = self._normalize_account_id(account)
        accounts = self.accounts()
        if account not in accounts:
            raise ValueError("业务 CPA 不存在：{}".format(account))
        if not accounts[account]["group_enabled"]:
            raise ValueError("CPA 账号已停用：{}".format(account))
        active = [
            item for item in self.active_records()
            if item["user"] == user and item["account"] == account
        ]
        if not active:
            raise ValueError("用户尚未关联该 CPA 账号")
        if len({item["key"] for item in self.active_records() if item["user"] == user}) != 1:
            raise ValueError("用户尚未完成单 Key 迁移")

        original = self._read_routes()
        updated = dict(original)
        updated[user] = account
        self._write_routes(updated)
        try:
            self._render_gateway_key_map(accounts=accounts)
            if apply:
                self.publish_auth_snapshot(wait=False)
        except Exception:
            self._write_routes(original)
            self._render_gateway_key_map(accounts=accounts)
            if apply:
                try:
                    self.publish_auth_snapshot(wait=False)
                except Exception:
                    pass
            raise
        return {
            "user": user,
            "group_id": account,
            "group_name": accounts[account]["group_name"],
            "account": account,
            "updated_at": int(time.time()),
        }

    def set_user_routes(
        self,
        assignments,
        expected_routes=None,
        apply=True,
        wait_for_gateway=False,
    ):
        """Atomically move multiple unified user keys and publish one auth snapshot."""
        if not isinstance(assignments, dict):
            raise ValueError("批量用户路由必须为对象")
        expected_routes = expected_routes or {}
        if not isinstance(expected_routes, dict):
            raise ValueError("批量用户原路由必须为对象")

        accounts = self.accounts()
        normalized = {}
        normalized_expected = {}
        for raw_user, raw_account in assignments.items():
            user = self._normalize_user(str(raw_user))
            account = self._normalize_account_id(raw_account)
            if account not in accounts:
                raise ValueError("业务 CPA 不存在：{}".format(account))
            if not accounts[account]["group_enabled"]:
                raise ValueError("CPA 账号已停用：{}".format(account))
            normalized[user] = account
        for raw_user, raw_account in expected_routes.items():
            user = self._normalize_user(str(raw_user))
            normalized_expected[user] = self._normalize_account_id(raw_account)

        original = self._read_routes()
        for user, expected in normalized_expected.items():
            if original.get(user) != expected:
                raise ValueError("用户路由已变化，请重新生成迁移计划：{}".format(user))

        active = self.active_records()
        by_user = {}
        for item in active:
            by_user.setdefault(item["user"], []).append(item)
        changed = {}
        for user, account in normalized.items():
            records = by_user.get(user, [])
            if not records:
                raise ValueError("{} 没有启用中的 Key".format(user))
            if len({item["key"] for item in records}) != 1:
                raise ValueError("用户尚未完成单 Key 迁移：{}".format(user))
            if not any(item["account"] == account for item in records):
                raise ValueError("用户尚未关联该 CPA 账号：{} -> {}".format(user, account))
            if original.get(user) != account:
                changed[user] = account

        if not changed:
            return {
                "moved_users": 0,
                "destinations": {},
                "snapshot": None,
                "updated_at": int(time.time()),
            }

        updated = dict(original)
        updated.update(changed)
        snapshot = None
        self._write_routes(updated)
        try:
            self._render_gateway_key_map(accounts=accounts, active=active)
            if apply:
                snapshot = self.publish_auth_snapshot(
                    wait=bool(wait_for_gateway),
                )
        except Exception:
            self._write_routes(original)
            self._render_gateway_key_map(accounts=accounts, active=active)
            if apply:
                try:
                    self.publish_auth_snapshot(wait=False)
                except Exception:
                    pass
            raise

        destinations = {}
        for account in changed.values():
            destinations[account] = destinations.get(account, 0) + 1
        return {
            "moved_users": len(changed),
            "destinations": dict(sorted(destinations.items())),
            "snapshot": snapshot,
            "updated_at": int(time.time()),
        }

    def _latest_user_accounts(self):
        latest = {}
        if not self.access_log.exists():
            return latest
        with self.access_log.open(encoding="utf-8", errors="replace") as handle:
            for line in handle:
                fields = line.rstrip("\n").split("\t")
                if len(fields) < 3:
                    continue
                try:
                    timestamp = float(fields[0])
                except ValueError:
                    continue
                label, account = fields[1], fields[2]
                if ":" not in label:
                    continue
                user = label.rsplit(":", 1)[0]
                if timestamp > latest.get(user, (0, ""))[0]:
                    latest[user] = (timestamp, account)
        return {user: account for user, (_, account) in latest.items()}

    def migrate_single_user_keys(self, apply=True, dry_run=False):
        records = self._read_registry()
        accounts = self.accounts()
        users = sorted({item["user"] for item in records})
        original_routes = self._read_routes()
        latest_accounts = self._latest_user_accounts()
        updated = [dict(item) for item in records]
        updated_routes = dict(original_routes)
        migrated_users = 0
        reused_keys = 0
        now = int(time.time())

        for user in users:
            active = [item for item in updated if item["user"] == user and item["status"] == "active"]
            if not active:
                continue
            route = original_routes.get(user) or latest_accounts.get(user) or self.default_group(accounts)
            if route not in accounts or not accounts[route]["group_enabled"]:
                route = self.default_group(accounts)
            routed = [item for item in active if item["account"] == route]
            selected = max(routed or active, key=lambda item: (item["updated_at"], item["created_at"]))
            key = selected["key"]
            already_single = (
                len({item["key"] for item in active}) == 1
                and {item["account"] for item in active} == set(accounts)
                and len(active) == len(accounts)
            )
            updated_routes[user] = route
            if already_single:
                continue
            for item in active:
                item["status"] = "rotated"
                item["updated_at"] = now
            updated.extend(
                self._new_record("{}:{}".format(user, account), key=key)
                for account in accounts
            )
            migrated_users += 1
            reused_keys += 1

        result = {
            "users": len(users),
            "migrated_users": migrated_users,
            "reused_keys": reused_keys,
            "default_group": self.default_group(accounts),
            "dry_run": bool(dry_run),
        }
        if dry_run:
            return result

        self._write_routes(updated_routes)
        try:
            self._commit_records(records, updated, apply=apply)
        except Exception:
            self._write_routes(original_routes)
            raise
        return result

    def _render_gateway_key_map(self, accounts=None, active=None):
        accounts = accounts or self.accounts()
        active = active if active is not None else self.active_records()
        routes = self._read_routes()
        by_user = {}
        for item in active:
            by_user.setdefault(item["user"], []).append(item)
        map_lines = ["# 自动生成；不要手工编辑。完整 Key 不会写入访问日志。"]
        emitted = set()
        for user in sorted(by_user):
            items = by_user[user]
            keys = {item["key"] for item in items}
            if len(keys) == 1:
                account = routes.get(user)
                if account not in accounts or not accounts[account]["group_enabled"]:
                    continue
                if account not in {item["account"] for item in items}:
                    # 未选择账号的统一 Key 不进入网关映射；用户仍可进入使用中心完成选择。
                    continue
                key = next(iter(keys))
                candidates = [item for item in items if item["account"] == account]
                label = (candidates[0] if candidates else items[0])["user"] + ":" + account
                pairs = [(key, label)]
            else:
                # 兼容迁移前的历史数据：旧 Key 仍按原 CPA 路由。
                pairs = [(item["key"], item["label"]) for item in items]
            for key, label in pairs:
                if key in emitted:
                    continue
                emitted.add(key)
                map_lines.append(
                    "{} {};".format(
                        json.dumps("Bearer " + key),
                        json.dumps(label),
                    )
                )
        self._atomic_text(
            self.gateway_key_map_path,
            "\n".join(map_lines) + "\n",
            0o600,
        )

    def render(self):
        """从本地账号与 Key 注册表生成 CPA、Compose 和网关配置。"""
        self.ensure_layout()
        accounts = self.accounts()
        configuration = self.configuration()["values"]
        account_proxies = {
            account: self.account_proxy_configuration(account, metadata)
            for account, metadata in accounts.items()
        }
        active = self.active_records()
        internal_keys = self.sync_internal_keys({item["user"] for item in active})
        management_key = self.management_key()
        for account, metadata in accounts.items():
            # Every CPA accepts only stable per-user internal credentials. The
            # external registry remains untouched for rollback compatibility.
            keys = sorted(item["key"] for item in internal_keys.values())
            proxy_url = account_proxies[account]["effective_url"]
            lines = [
                "# 由 scripts/cliproxy.py 自动生成，请通过 key 子命令修改。",
                "host: \"\"",
                "port: 8317",
                "tls:",
                "  enable: false",
                "  cert: \"\"",
                "  key: \"\"",
                "remote-management:",
                "  allow-remote: true",
                "  secret-key: {}".format(json.dumps(management_key)),
                "  disable-control-panel: false",
                # The management CPA owns updates for the shared panel asset.
                # Business CPAs mount that asset read-only to keep generated
                # configs immutable while still serving management.html.
                "  disable-auto-update-panel: true",
                "auth-dir: \"~/.cli-proxy-api\"",
            ]
            if keys:
                lines.append("api-keys:")
                lines.extend("  - {}".format(json.dumps(key)) for key in keys)
            else:
                lines.append("api-keys: []")
            disable_image_generation = configuration["cpa.disable_image_generation"]
            rendered_disable_image_generation = (
                json.dumps(disable_image_generation)
                if disable_image_generation == "chat"
                else disable_image_generation
            )
            lines.extend(
                [
                    "debug: {}".format(str(configuration["cpa.debug"]).lower()),
                    "logging-to-file: {}".format(
                        str(configuration["cpa.logging_to_file"]).lower()
                    ),
                    "logs-max-total-size-mb: {}".format(
                        configuration["cpa.logs_max_total_size_mb"]
                    ),
                    "error-logs-max-files: {}".format(
                        configuration["cpa.error_logs_max_files"]
                    ),
                    "usage-statistics-enabled: {}".format(
                        str(configuration["cpa.usage_statistics_enabled"]).lower()
                    ),
                    "disable-image-generation: {}".format(
                        rendered_disable_image_generation
                    ),
                    "redis-usage-queue-retention-seconds: {}".format(
                        configuration["cpa.usage_queue_retention_seconds"]
                    ),
                    "proxy-url: {}".format(json.dumps(proxy_url)),
                    "request-retry: {}".format(configuration["cpa.request_retry"]),
                    "max-retry-credentials: {}".format(
                        configuration["cpa.max_retry_credentials"]
                    ),
                    "max-retry-interval: {}".format(
                        configuration["cpa.max_retry_interval"]
                    ),
                    "transient-error-cooldown-seconds: {}".format(
                        configuration["cpa.transient_error_cooldown_seconds"]
                    ),
                    "routing:",
                    "  strategy: \"round-robin\"",
                    "  session-affinity: {}".format(
                        str(configuration["cpa.session_affinity"]).lower()
                    ),
                    "  session-affinity-ttl: {}".format(
                        json.dumps(configuration["cpa.session_affinity_ttl"])
                    ),
                    "# 账号：{}，上游邮箱：{}".format(account, metadata["email"]),
                    "",
                ]
            )
            path = self.root / "configs" / "{}.yaml".format(account)
            self._atomic_text(path, "\n".join(lines), 0o600)

        self._render_gateway_key_map(accounts=accounts, active=active)
        self.publish_auth_snapshot(wait=False)

        account_map = ["# 自动生成；Key 标签到业务 CPA。"]
        backend_map = ["# 自动生成；业务 CPA 到容器后端。"]
        for account in accounts:
            account_map.append("~:{}$ {};".format(account, account))
            backend_map.append("{} cliproxy-{}:8317;".format(account, account))
        self._atomic_text(
            self.gateway_accounts_map_path, "\n".join(account_map) + "\n", 0o600
        )
        self._atomic_text(
            self.gateway_backends_map_path, "\n".join(backend_map) + "\n", 0o600
        )

        public_payload = {
            "accounts": [
                {
                    "id": account,
                    "port": metadata["port"],
                    "group_name": metadata["group_name"],
                    "group_enabled": metadata["group_enabled"],
                }
                for account, metadata in accounts.items()
            ]
        }
        self._atomic_text(
            self.public_accounts_path,
            json.dumps(public_payload, ensure_ascii=False, indent=2) + "\n",
            0o644,
        )
        self.render_public_site_configuration()
        self._atomic_text(
            self.accounts_compose_path,
            self._render_accounts_compose(accounts),
            0o600,
        )

    @staticmethod
    def _render_accounts_compose(accounts):
        lines = [
            "# 由 scripts/cliproxy.py 自动生成，请勿手工编辑。",
            "services:" if accounts else "services: {}",
        ]
        for account, metadata in accounts.items():
            service = "cliproxy-{}".format(account)
            lines.extend(
                [
                    "  {}:".format(service),
                    "    image: ${CLIPROXY_IMAGE:?state/compose.env missing; run codex-cpa render}",
                    "    container_name: ${{INSTANCE_NAME:-cliproxy}}-{}".format(account),
                    "    restart: unless-stopped",
                    "    logging:",
                    "      driver: json-file",
                    "      options:",
                    "        max-size: \"20m\"",
                    "        max-file: \"3\"",
                    "    command: [\"./CLIProxyAPI\", \"-config\", \"/CLIProxyAPI/configs/{}.yaml\"]".format(account),
                    "    ports:",
                    "      - \"${{BUSINESS_CPA_LISTEN_ADDRESS:?state/compose.env missing}}:{}:8317\"".format(
                        metadata["port"]
                    ),
                    "    volumes:",
                    "      - ./configs/{0}.yaml:/CLIProxyAPI/configs/{0}.yaml:ro".format(account),
                    "      - ./management/config/static:/CLIProxyAPI/configs/static:ro",
                    "      - ./auth/{0}:/root/.cli-proxy-api".format(account),
                    "      - ./logs/{0}:/CLIProxyAPI/logs".format(account),
                ]
            )
            lines.extend(
                [
                    "    networks:",
                    "      - backend",
                    "",
                ]
            )
        return "\n".join(lines).rstrip() + "\n"

    @staticmethod
    def _atomic_text(path, content, mode):
        temporary = path.with_name(
            ".{}.{}.tmp".format(path.name, uuid.uuid4().hex)
        )
        try:
            temporary.write_text(content, encoding="utf-8")
            os.chmod(temporary, mode)
            os.replace(str(temporary), str(path))
        finally:
            try:
                temporary.unlink()
            except FileNotFoundError:
                pass

    def compose_command(self):
        return [
            "docker",
            "compose",
            "--project-directory",
            str(self.root),
            "--env-file",
            str(self.root / ".env"),
            "--env-file",
            str(self.compose_env_path),
            "-f",
            str(self.root / "docker-compose.yml"),
            "-f",
            str(self.accounts_compose_path),
        ]

    def compose(self, *args, check=True, capture=False):
        command = self.compose_command() + list(args)
        return subprocess.run(
            command,
            cwd=str(self.root),
            check=check,
            text=True,
            stdout=subprocess.PIPE if capture else None,
            stderr=subprocess.PIPE if capture else None,
        )

    def _docker(self, *args, check=True, capture=False, env=None):
        return subprocess.run(
            ["docker"] + list(args),
            cwd=str(self.root),
            check=check,
            text=True,
            env=env,
            stdout=subprocess.PIPE if capture else None,
            stderr=subprocess.PIPE if capture else None,
        )

    def _docker_json(self, *args):
        rows = self._docker_json_rows(*args)
        return rows[0] if rows else None

    def _docker_json_rows(self, *args):
        """Return JSON objects from Docker array or JSON-lines output."""
        result = self._docker(*args, check=False, capture=True)
        raw = (result.stdout or "").strip()
        if result.returncode != 0 or not raw:
            return []
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError:
            rows = []
            for line in raw.splitlines():
                try:
                    item = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if isinstance(item, dict):
                    rows.append(item)
            return rows
        if isinstance(payload, list):
            return [item for item in payload if isinstance(item, dict)]
        return [payload] if isinstance(payload, dict) else []

    @staticmethod
    def _short_image_id(value):
        raw = str(value or "")
        return raw.split(":", 1)[-1][:12] if raw else ""

    @staticmethod
    def _image_repository(image_ref):
        value = str(image_ref or "").split("@", 1)[0]
        slash = value.rfind("/")
        colon = value.rfind(":")
        return value[:colon] if colon > slash else value

    @staticmethod
    def _semantic_version_key(value):
        match = re.fullmatch(
            r"v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?",
            str(value or "").strip(),
        )
        if match is None:
            return None
        prerelease = match.group(4)
        prerelease_key = tuple(
            (0, int(part)) if part.isdigit() else (1, part)
            for part in (prerelease or "").split(".")
            if part
        )
        return (
            int(match.group(1)),
            int(match.group(2)),
            int(match.group(3)),
            1 if prerelease is None else 0,
            prerelease_key,
        )

    def _resolve_cliproxy_image_identity(self, image_ref, image=None):
        image = image or self._docker_json("image", "inspect", image_ref)
        if not image:
            raise ValueError("无法读取 CLIProxyAPI 镜像信息")
        image_id = str(image.get("Id") or "").strip()
        repo_digests = [
            str(value).strip()
            for value in (image.get("RepoDigests") or [])
            if str(value).strip()
        ]
        source_repository = self._image_repository(image_ref)
        repo_digest = next(
            (
                value
                for value in repo_digests
                if value.rsplit("@", 1)[0] == source_repository
            ),
            repo_digests[0] if repo_digests else "",
        )
        labels = ((image.get("Config") or {}).get("Labels") or {})
        labels = labels if isinstance(labels, dict) else {}
        label_version = str(
            labels.get("org.opencontainers.image.version") or ""
        ).strip()
        version = (
            label_version
            if SEMANTIC_VERSION_RE.fullmatch(label_version)
            else ""
        )
        commit = str(labels.get("org.opencontainers.image.revision") or "").strip()
        built_at = str(labels.get("org.opencontainers.image.created") or "").strip()
        config = image.get("Config") or {}
        entrypoint = config.get("Entrypoint") or []
        default_command = config.get("Cmd") or []
        if isinstance(entrypoint, str):
            entrypoint = [entrypoint]
        if isinstance(default_command, str):
            default_command = [default_command]
        default_arguments = [
            str(value) for value in default_command if str(value).strip()
        ]
        # Passing `-h` after the image only works when the image declares an
        # ENTRYPOINT. CLIProxyAPI publishes its executable as CMD, so Docker
        # would otherwise try to execute a binary literally named `-h`.
        probe_arguments = (
            ["-h"]
            if entrypoint
            else [*(default_arguments or ["./CLIProxyAPI"]), "-h"]
        )
        command = [
            "docker",
            "run",
            "--rm",
            "--network",
            "none",
            "--read-only",
            "--cap-drop",
            "ALL",
            "--security-opt",
            "no-new-privileges",
            "--pids-limit",
            "64",
            image_id or image_ref,
            *probe_arguments,
        ]
        try:
            result = subprocess.run(
                command,
                cwd=str(self.root),
                check=False,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=CLIPROXY_IDENTITY_TIMEOUT_SECONDS,
            )
            output = ((result.stdout or "") + "\n" + (result.stderr or ""))[
                :CLIPROXY_IDENTITY_MAX_OUTPUT_BYTES
            ]
            match = CLIPROXY_VERSION_BANNER_RE.search(output)
            if match:
                banner_version, banner_commit, banner_built_at = (
                    str(match.group(index) or "").strip() for index in range(1, 4)
                )
                if not version and SEMANTIC_VERSION_RE.fullmatch(banner_version):
                    version = banner_version
                if not commit:
                    commit = banner_commit
                if not built_at:
                    built_at = banner_built_at
        except (OSError, subprocess.TimeoutExpired):
            pass

        if not version:
            version_tags = []
            for value in image.get("RepoTags") or []:
                repository = self._image_repository(value)
                tag = str(value).split("@", 1)[0][len(repository) + 1 :]
                if repository == source_repository and SEMANTIC_VERSION_RE.fullmatch(tag):
                    version_tags.append(tag)
            if version_tags:
                version = max(version_tags, key=self._semantic_version_key)

        resolved_ref = repo_digest or image_id or str(image_ref)
        if version and repo_digest and "@sha256:" in repo_digest:
            repository, digest = repo_digest.rsplit("@", 1)
            if re.fullmatch(r"[A-Za-z0-9._-]+", version):
                resolved_ref = "{}:{}@{}".format(repository, version, digest)
        return {
            "source_ref": str(image_ref),
            "version": version,
            "commit": commit,
            "built_at": built_at,
            "image_id": image_id,
            "image_short_id": self._short_image_id(image_id),
            "repo_digest": repo_digest,
            "repo_digests": repo_digests,
            "resolved_ref": resolved_ref,
        }

    def _matching_cliproxy_identity(self, name, image_id):
        record = self.cliproxy_image_runtime_state().get(name) or {}
        if not isinstance(record, dict) or str(record.get("image_id") or "") != str(image_id or ""):
            return {}
        return dict(record)

    def _known_cliproxy_identity(self, image_id, state=None):
        image_id = str(image_id or "")
        if not image_id:
            return {}
        state = state if isinstance(state, dict) else self.cliproxy_image_runtime_state()
        for name in ("applied", "candidate"):
            record = state.get(name) or {}
            if isinstance(record, dict) and str(record.get("image_id") or "") == image_id:
                return dict(record)
        history = state.get("history") or {}
        record = history.get(image_id) if isinstance(history, dict) else None
        return dict(record) if isinstance(record, dict) else {}

    def _write_cliproxy_candidate(self, identity):
        state = self.cliproxy_image_runtime_state()
        state["candidate"] = {
            **dict(identity),
            "pulled_at": int(time.time()),
        }
        self.store.write_runtime_state("cliproxy_image", state)
        return dict(state["candidate"])

    def _commit_cliproxy_applied(self, identity):
        previous = self.store.read_runtime_state("cliproxy_image", None)
        state = dict(previous) if isinstance(previous, dict) else {}
        history = dict(state.get("history") or {})
        for record in (state.get("applied"), identity):
            if not isinstance(record, dict):
                continue
            image_id = str(record.get("image_id") or "")
            if image_id:
                history[image_id] = dict(record)
        if len(history) > 32:
            history = dict(
                sorted(
                    history.items(),
                    key=lambda item: max(
                        int(item[1].get("applied_at") or 0),
                        int(item[1].get("pulled_at") or 0),
                    ),
                    reverse=True,
                )[:32]
            )
        state["history"] = history
        state["candidate"] = {
            **dict(identity),
            "pulled_at": int((state.get("candidate") or {}).get("pulled_at") or time.time()),
        }
        state["applied"] = {
            **dict(identity),
            "applied_at": int(time.time()),
        }
        self.store.write_runtime_state("cliproxy_image", state)
        try:
            self.render_compose_environment()
        except BaseException:
            if previous is None:
                self.store.delete_runtime_state("cliproxy_image")
            else:
                self.store.write_runtime_state("cliproxy_image", previous)
            raise
        return dict(state["applied"])

    def cliproxy_image_status(self):
        image_ref = self.configuration()["values"]["runtime.cliproxy_image"]
        local = self._docker_json("image", "inspect", image_ref)
        local_id = str((local or {}).get("Id", ""))
        image_state = self.cliproxy_image_runtime_state()
        candidate_record = image_state.get("candidate") or {}
        candidate = (
            dict(candidate_record)
            if isinstance(candidate_record, dict)
            and str(candidate_record.get("image_id") or "") == local_id
            else {}
        )
        applied = image_state.get("applied") or {}
        applied = dict(applied) if isinstance(applied, dict) else {}
        account_metadata = self.accounts()
        services = self.services()
        container_names = {
            account: self.account_container_name(account) for account in services
        }
        listed_containers = self._docker_json_rows(
            "container", "ls", "-a", "--format", "json"
        )
        existing_names = {
            name
            for item in listed_containers
            for name in str(item.get("Names", item.get("Name", ""))).split(",")
            if name
        }
        inspect_names = [
            name for name in container_names.values() if name in existing_names
        ]
        container_rows = (
            self._docker_json_rows("container", "inspect", *inspect_names)
            if inspect_names
            else []
        )
        containers = {
            str(item.get("Name", "")).lstrip("/"): item for item in container_rows
        }
        image_rows = self._docker_json_rows("image", "ls", "--format", "json")
        available_image_refs = {
            "{}:{}".format(item.get("Repository"), item.get("Tag"))
            for item in image_rows
            if item.get("Repository") not in (None, "", "<none>")
            and item.get("Tag") not in (None, "", "<none>")
        }
        accounts = []
        for account, service in services.items():
            container = containers.get(container_names[account])
            image_id = str((container or {}).get("Image", ""))
            state = (container or {}).get("State") or {}
            rollback_ref = self.rollback_image_ref(account)
            account_identity = self._known_cliproxy_identity(image_id, state=image_state)
            accounts.append(
                {
                    "account": account,
                    "service": service,
                    "enabled": bool(account_metadata[account]["group_enabled"]),
                    "container_exists": bool(container),
                    "running": bool(state.get("Running")),
                    "state": str(state.get("Status", "missing")),
                    "image_ref": str(((container or {}).get("Config") or {}).get("Image", "")),
                    "image_id": image_id,
                    "image_short_id": self._short_image_id(image_id),
                    "version": str(account_identity.get("version") or ""),
                    "using_target": bool(local_id and image_id == local_id),
                    "rollback_available": rollback_ref in available_image_refs,
                }
            )
        eligible_accounts = [item for item in accounts if item["enabled"]]
        return {
            "target_image": image_ref,
            "update_channel": image_ref,
            "candidate": candidate,
            "applied": applied,
            "local_image": {
                "available": bool(local),
                "id": local_id,
                "short_id": self._short_image_id(local_id),
                "created": str((local or {}).get("Created", "")),
                "repo_digests": list((local or {}).get("RepoDigests") or []),
                "version": str(candidate.get("version") or ""),
                "commit": str(candidate.get("commit") or ""),
                "built_at": str(candidate.get("built_at") or ""),
                "resolved_ref": str(candidate.get("resolved_ref") or ""),
            },
            "accounts": accounts,
            "running_count": sum(item["running"] for item in eligible_accounts),
            "current_count": sum(
                item["running"] and item["using_target"] for item in eligible_accounts
            ),
            "outdated_count": sum(
                item["running"] and not item["using_target"]
                for item in eligible_accounts
            ),
        }

    def pull_cliproxy_image(self):
        with self.runtime_operation_lock("CPA 镜像拉取"):
            return self._pull_cliproxy_image()

    def _pull_cliproxy_image(self):
        image_ref = self.configuration()["values"]["runtime.cliproxy_image"]
        print("正在拉取 CPA 镜像：{}".format(image_ref), flush=True)
        self._docker("pull", image_ref)
        image = self._docker_json("image", "inspect", image_ref)
        if not image:
            raise ValueError("镜像拉取后仍无法读取本地镜像信息")
        identity = self._resolve_cliproxy_image_identity(image_ref, image=image)
        candidate = self._write_cliproxy_candidate(identity)
        print(
            "镜像已就绪：{} · {} ({})".format(
                image_ref,
                candidate.get("version") or "版本未知",
                self._short_image_id(image.get("Id")),
            ),
            flush=True,
        )
        return candidate

    def _compose_with_image(self, image_ref, *args):
        environment = os.environ.copy()
        environment["CLIPROXY_IMAGE"] = image_ref
        return subprocess.run(
            self.compose_command() + list(args),
            cwd=str(self.root),
            check=True,
            text=True,
            env=environment,
        )

    def _probe_account_service(self, account, timeout=45):
        accounts = self.accounts()
        if account not in accounts:
            raise ValueError("未知账号：{}".format(account))
        service = self.services()[account]
        # 业务 CPA 只接受稳定的用户内部 Key；外部 Key 仅由网关鉴权并转换。
        # 镜像更新或容器重建后的直连探测必须使用内部 Key，否则会把正常
        # 的新鉴权架构误判成 401。
        internal = self.sync_internal_keys()
        record = next(iter(internal.values()), None)
        candidates = [
            "http://{}:8317/v1/models".format(service),
            "http://127.0.0.1:{}/v1/models".format(accounts[account]["port"]),
        ]
        deadline = time.time() + max(1, int(timeout))
        last_error = "服务尚未就绪"
        while time.time() < deadline:
            container = self._docker_json(
                "container", "inspect", self.account_container_name(account)
            )
            state = (container or {}).get("State") or {}
            if not state.get("Running"):
                last_error = "容器未运行"
                time.sleep(1)
                continue
            if not record:
                print(
                    "{} 已运行；当前没有活跃用户内部 Key，跳过模型列表验证".format(
                        account
                    ),
                    flush=True,
                )
                return
            for url in candidates:
                request = urllib.request.Request(
                    url,
                    headers={"Authorization": "Bearer " + record["key"]},
                )
                try:
                    with urllib.request.urlopen(request, timeout=3) as response:
                        payload = json.load(response)
                    models = payload.get("data") if isinstance(payload, dict) else None
                    if response.status == 200 and isinstance(models, list) and models:
                        print("{} 验证通过：MODELS {}".format(account, len(models)), flush=True)
                        return
                    last_error = "模型列表为空"
                except Exception as error:
                    last_error = "{}: {}".format(type(error).__name__, error)
            time.sleep(1)
        raise ValueError("{} 更新后验证失败：{}".format(account, last_error))

    def update_cliproxy_image(self, target="all"):
        with self.runtime_operation_lock("CPA 镜像更新"):
            return self._update_cliproxy_image(target)

    def _update_cliproxy_image(self, target="all"):
        target = str(target or "all").strip().lower()
        accounts = self.accounts()
        if target != "all" and target not in accounts:
            raise ValueError("镜像更新必须选择 all 或有效 CPA 账号")
        if target != "all" and not accounts[target]["group_enabled"]:
            raise ValueError("CPA 账号已停用，不能更新镜像：{}".format(target))
        image_ref = self.configuration()["values"]["runtime.cliproxy_image"]
        target_image = self._docker_json("image", "inspect", image_ref)
        if not target_image:
            raise ValueError("目标镜像尚未拉取，请先执行“拉取镜像”")
        target_image_id = str(target_image.get("Id", ""))
        identity = self._matching_cliproxy_identity("candidate", target_image_id)
        if not identity:
            identity = self._write_cliproxy_candidate(
                self._resolve_cliproxy_image_identity(image_ref, image=target_image)
            )
        resolved_ref = str(identity.get("resolved_ref") or "").strip()
        if not resolved_ref:
            raise ValueError("目标镜像缺少可应用的不可变标识")
        if target == "all":
            selected = []
            for account, metadata in accounts.items():
                if not metadata["group_enabled"]:
                    print("跳过 {}：CPA 已停用".format(account), flush=True)
                    continue
                selected.append(account)
        else:
            selected = [target]
        snapshots = []
        already_current = []
        for account in selected:
            service = self.services()[account]
            container = self._docker_json(
                "container", "inspect", self.account_container_name(account)
            )
            state = (container or {}).get("State") or {}
            if not container or not state.get("Running"):
                print("跳过 {}：CPA 未运行；下次启动会使用目标镜像".format(account), flush=True)
                continue
            old_image_id = str(container.get("Image", ""))
            if old_image_id == target_image_id:
                print("跳过 {}：已经运行目标镜像".format(account), flush=True)
                already_current.append(account)
                continue
            rollback_ref = self.rollback_image_ref(account)
            self._docker("image", "tag", old_image_id, rollback_ref)
            snapshots.append(
                {
                    "account": account,
                    "service": service,
                    "old_image_id": old_image_id,
                    "rollback_ref": rollback_ref,
                }
            )
        if not snapshots:
            if not already_current:
                print("没有运行中的 CPA；未改变已应用版本", flush=True)
                return
            for account in already_current:
                self._probe_account_service(account)
            self._commit_cliproxy_applied(identity)
            print("运行中的 CPA 已验证；已固定目标版本", flush=True)
            return

        attempted = []
        try:
            for snapshot in snapshots:
                account = snapshot["account"]
                service = snapshot["service"]
                attempted.append(snapshot)
                print(
                    "正在更新 {}：{} -> {}".format(
                        account,
                        self._short_image_id(snapshot["old_image_id"]),
                        self._short_image_id(target_image_id),
                    ),
                    flush=True,
                )
                self._compose_with_image(
                    resolved_ref,
                    "up",
                    "-d",
                    "--no-deps",
                    "--force-recreate",
                    service,
                )
                self._probe_account_service(account)
            # Commit the Compose projection only after every selected running
            # CPA has passed its model probe. A failed atomic write follows the
            # same rollback path as a failed container update.
            self._commit_cliproxy_applied(identity)
        except BaseException as error:
            print("镜像更新失败，正在恢复已处理的 CPA：{}".format(error), flush=True)
            rollback_errors = []
            for snapshot in reversed(attempted):
                try:
                    self._compose_with_image(
                        snapshot["rollback_ref"],
                        "up",
                        "-d",
                        "--no-deps",
                        "--force-recreate",
                        snapshot["service"],
                    )
                    self._probe_account_service(snapshot["account"])
                    print("已恢复 {}".format(snapshot["account"]), flush=True)
                except Exception as rollback_error:
                    rollback_errors.append(
                        "{}: {}".format(snapshot["account"], rollback_error)
                    )
            if rollback_errors:
                raise ValueError(
                    "镜像更新失败，且部分回退失败：{}".format("; ".join(rollback_errors))
                ) from error
            raise
        print("CPA 镜像更新完成：{} 个".format(len(attempted)), flush=True)

    def apply_changes(self, restart_containers=True):
        self.render()
        # 先在临时容器中校验配置，避免错误配置打断现有流量。
        self.compose("config", "--quiet")
        # 默认重建业务 CPA，确保容器重新加载 api-keys。
        # 批量导入可传 restart_containers=False：只渲染配置并热重载网关，
        # 依赖 CLIProxyAPI 对配置文件的 file watcher 生效。
        if restart_containers:
            self.compose("up", "-d", "--force-recreate", *self.services().values())
        self._reload_gateway_if_running()

    def _reload_gateway_if_running(self):
        gateway_running = self.compose(
            "ps", "--status", "running", "--services", check=False, capture=True
        ).stdout.splitlines()
        for service in ("gateway-blue", "gateway-green", "gateway"):
            if service in gateway_running:
                self.compose("exec", "-T", service, "openresty", "-t")
                self.compose("exec", "-T", service, "openresty", "-s", "reload")

    def _reset_access_log_cache(self):
        self._access_log_identity = None
        self._access_log_offset = 0
        self._access_log_prefix = b""
        self._access_log_rows = []
        self._access_log_last_now = None

    @staticmethod
    def _parse_access_log_line(raw_line):
        fields = raw_line.decode("utf-8", errors="replace").rstrip("\r\n").split("\t")
        if len(fields) < 3:
            return None
        try:
            timestamp = float(fields[0])
        except ValueError:
            return None
        try:
            status = int(fields[3]) if len(fields) >= 5 else None
        except ValueError:
            status = None
        return timestamp, fields[1], fields[2], status

    def recent_access_rows(self, window_seconds, now=None):
        """Read recent gateway access rows, incrementally following the log file."""
        now = time.time() if now is None else float(now)
        window_seconds = max(1, int(window_seconds))
        retention_seconds = max(60 * 60, window_seconds)
        with self._access_log_lock:
            if (
                retention_seconds > self._access_log_retention_seconds
                or (
                    self._access_log_last_now is not None
                    and now < self._access_log_last_now
                )
            ):
                self._reset_access_log_cache()
            self._access_log_retention_seconds = retention_seconds
            retention_cutoff = now - self._access_log_retention_seconds

            try:
                handle = self.access_log.open("rb")
            except OSError:
                self._reset_access_log_cache()
                return []

            with handle:
                stat = os.fstat(handle.fileno())
                identity = (stat.st_dev, stat.st_ino)
                prefix_length = min(self._access_log_offset, 256)
                current_prefix = handle.read(prefix_length) if prefix_length else b""
                if (
                    identity != self._access_log_identity
                    or stat.st_size < self._access_log_offset
                    or current_prefix != self._access_log_prefix[:prefix_length]
                ):
                    self._reset_access_log_cache()
                    self._access_log_identity = identity

                handle.seek(self._access_log_offset)
                appended = handle.read()
                complete_length = appended.rfind(b"\n") + 1
                if complete_length:
                    for raw_line in appended[:complete_length].splitlines():
                        row = self._parse_access_log_line(raw_line)
                        if row is not None and row[0] >= retention_cutoff:
                            self._access_log_rows.append(row)
                    self._access_log_offset += complete_length
                handle.seek(0)
                self._access_log_prefix = handle.read(
                    min(self._access_log_offset, 256)
                )
                self._access_log_identity = identity

            self._access_log_rows = [
                row for row in self._access_log_rows if row[0] >= retention_cutoff
            ]
            self._access_log_last_now = now
            cutoff = now - window_seconds
            return [
                row
                for row in self._access_log_rows
                if cutoff <= row[0] <= now + 1
            ]

    def active_stats(self, window_seconds, now=None):
        now = time.time() if now is None else float(now)
        accounts = self.accounts()
        labels = {account: set() for account in accounts}
        request_counts = {account: 0 for account in accounts}
        for _, label, account, _ in self.recent_access_rows(window_seconds, now=now):
            if account in labels:
                labels[account].add(label)
                request_counts[account] += 1
        return {
            account: {
                "account_email": accounts[account]["email"],
                "count": len(labels[account]),
                "requests": request_counts[account],
                "labels": sorted(labels[account]),
                "users": sorted(label.rsplit(":", 1)[0] for label in labels[account]),
            }
            for account in accounts
        }

    def inflight_stats(self):
        accounts = self.accounts()
        try:
            payload = self._inflight_stats_via_http()
        except Exception:
            # 兼容本地脚本和旧部署：内网监听器不可达或返回异常时，仍可通过
            # edge 容器读取统计；这里不输出异常，避免响应正文进入日志。
            result = self.compose(
                "exec", "-T", "edge", "wget", "-qO-",
                "http://127.0.0.1:8319/__stats", capture=True,
            )
            payload = self._validate_inflight_stats_payload(
                json.loads(result.stdout)
            )
        output = {
            account: {
                "account_email": accounts[account]["email"],
                "count": 0,
                "labels": [],
                "users": [],
            }
            for account in accounts
        }
        for item in payload:
            if item["account"] in output and item["inflight"] > 0:
                output[item["account"]]["labels"].append(item["label"])
        for item in output.values():
            item["labels"].sort()
            item["count"] = len(item["labels"])
            item["users"] = sorted(label.rsplit(":", 1)[0] for label in item["labels"])
        return output

    def _inflight_stats_via_http(self):
        request = urllib.request.Request(
            self.gateway_internal_url() + "/__stats",
            headers={"Accept": "application/json"},
        )
        with urllib.request.urlopen(
            request, timeout=INFLIGHT_STATS_HTTP_TIMEOUT_SECONDS
        ) as response:
            body = response.read(INFLIGHT_STATS_MAX_RESPONSE_BYTES + 1)
        if len(body) > INFLIGHT_STATS_MAX_RESPONSE_BYTES:
            raise ValueError("网关并发统计响应过大")
        return self._validate_inflight_stats_payload(json.loads(body))

    @staticmethod
    def _validate_inflight_stats_payload(payload):
        """在进入管理端聚合前校验内部统计响应。"""
        if not isinstance(payload, list):
            raise ValueError("网关并发统计响应格式错误")
        for item in payload:
            if not isinstance(item, dict):
                raise ValueError("网关并发统计响应格式错误")
            label = item.get("label")
            account = item.get("account")
            inflight = item.get("inflight")
            if (
                not isinstance(label, str)
                or not label
                or not isinstance(account, str)
                or not account
                or isinstance(inflight, bool)
                or not isinstance(inflight, (int, float))
                or not math.isfinite(inflight)
                or inflight < 0
            ):
                raise ValueError("网关并发统计响应格式错误")
        return payload

    def health(self):
        accounts = self.accounts()
        active = self.active_records()
        auth = self.auth_status()
        by_account = {}
        by_user = {}
        for record in active:
            by_user.setdefault(record["user"], []).append(record)
        for user, records in by_user.items():
            if len({item["key"] for item in records}) == 1:
                route = self.explicit_user_route(user, accounts=accounts)
                record = next(
                    (item for item in records if item["account"] == route),
                    None,
                )
                if record:
                    by_account.setdefault(route, record)
                continue
            # 迁移前的多 Key 用户仍按每条历史记录自己的账号路由。
            for record in records:
                by_account.setdefault(record["account"], record)
        probe_url = self.gateway_url() + INTERNAL_MODELS_PROBE_PATH
        for account, metadata in accounts.items():
            record = by_account.get(account)
            if not record:
                print("{} {} NO_KEY".format(account, metadata["email"]))
                continue
            request = urllib.request.Request(
                probe_url,
                headers={"Authorization": "Bearer " + record["key"]},
            )
            try:
                with urllib.request.urlopen(request, timeout=20) as response:
                    payload = json.load(response)
                    models = len(payload.get("data", [])) if isinstance(payload, dict) else 0
                    print(
                        "{} {} HTTP {} AUTH_FILES {} MODELS {}".format(
                            account,
                            metadata["email"],
                            response.status,
                            auth[account]["files"],
                            models,
                        )
                    )
            except urllib.error.HTTPError as error:
                print(
                    "{} {} HTTP {} AUTH_FILES {}".format(
                        account, metadata["email"], error.code, auth[account]["files"]
                    )
                )
            except Exception as error:  # 运维命令需要展示单账号错误，同时继续检查其余账号。
                print("{} {} ERROR {}".format(account, metadata["email"], error))

    def login(self, account):
        account = str(account).strip().lower()
        services = self.services()
        if account not in services:
            raise ValueError("未知账号：{}".format(account))
        auth_dir = self.root / "auth" / account

        def auth_snapshot():
            snapshot = {}
            for path in auth_dir.glob("*.json"):
                try:
                    stat = path.stat()
                except OSError:
                    continue
                snapshot[path.name] = (stat.st_mtime_ns, stat.st_size)
            return snapshot

        before = auth_snapshot()
        # 设备码模式最适合远程服务器：无需暴露 OAuth 回调端口。
        self.compose(
            "run",
            "--rm",
            "--no-deps",
            "-T",
            services[account],
            "./CLIProxyAPI",
            "-config",
            "/CLIProxyAPI/configs/{}.yaml".format(account),
            "-codex-device-login",
            "-no-browser",
        )
        # CLIProxyAPI v7.2.72 在配置加载失败时仍返回 0，不能只依赖进程
        # 退出码。设备授权成功必须新增或更新认证 JSON，否则将任务标为失败。
        if auth_snapshot() == before:
            raise ValueError("OAuth 授权未完成：没有检测到新增或更新的认证文件")

    def auth_status(self):
        """只统计隔离目录中的认证文件，不读取或输出令牌内容。"""
        return {
            account: {
                "account_email": metadata["email"],
                "files": len(list((self.root / "auth" / account).glob("*.json"))),
            }
            for account, metadata in self.accounts().items()
        }

    def verify_routing(self):
        """逐个验证启用 Key；结果只包含标签，不包含密钥。"""
        result = []
        for record in self.active_records():
            status = 0
            for gateway_url in self.gateway_probe_urls():
                request = urllib.request.Request(
                    gateway_url + INTERNAL_MODELS_PROBE_PATH,
                    headers={"Authorization": "Bearer " + record["key"]},
                )
                try:
                    with urllib.request.urlopen(request, timeout=20) as response:
                        status = response.status
                    break
                except urllib.error.HTTPError as error:
                    status = error.code
                    if status not in (403, 404):
                        break
                except Exception:
                    status = 0
            result.append(
                {"label": record["label"], "account": record["account"], "status": status}
            )
        return result

    def gateway_url(self):
        configured = os.environ.get("CLIPROXY_GATEWAY_URL", "").strip().rstrip("/")
        if configured:
            return configured
        port = self.configuration()["values"]["gateway.port"]
        return "http://127.0.0.1:{}".format(port)

    def gateway_internal_url(self):
        configured = os.environ.get("CLIPROXY_GATEWAY_INTERNAL_URL", "").strip().rstrip("/")
        if configured:
            return configured
        port = self.configuration()["values"]["gateway.internal_port"]
        return "http://127.0.0.1:{}".format(port)

    def gateway_probe_urls(self):
        urls = []
        for value in (self.gateway_internal_url(), self.gateway_url()):
            if value not in urls:
                urls.append(value)
        return urls


def read_env(path):
    values = {}
    if not path.exists():
        return values
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip()
    return values


def parse_duration(value):
    match = re.fullmatch(r"([1-9][0-9]*)([smhd])", value.strip().lower())
    if not match:
        raise ValueError("时间窗口格式应为 30s、5m、1h 或 7d")
    scale = {"s": 1, "m": 60, "h": 3600, "d": 86400}[match.group(2)]
    return int(match.group(1)) * scale


def print_records(records):
    print("IDENTIFIER\tACCOUNT\tACCOUNT_EMAIL\tUSER\tSTATUS\tCREATED_AT")
    for item in records:
        print(
            "{label}\t{account}\t{account_email}\t{user}\t{status}\t{created_at}".format(
                **item
            )
        )


def print_created(records):
    print("以下完整 Key 仅在创建/轮换时展示，请立即安全保存：")
    for item in records:
        print(
            "{}\t{}\t{}".format(
                item["label"], item["account_email"], item["key"]
            )
        )


def print_stats(stats, detail):
    print("ACCOUNT\tACCOUNT_EMAIL\tKEY_COUNT\tUSERS")
    for account in stats:
        item = stats[account]
        users = ",".join(item["users"]) if detail else "-"
        print(
            "{}\t{}\t{}\t{}".format(
                account, item["account_email"], item["count"], users
            )
        )


def print_auth_status(status):
    print("ACCOUNT\tACCOUNT_EMAIL\tAUTH_FILES\tSTATE")
    for account in status:
        item = status[account]
        state = "configured" if item["files"] > 0 else "pending"
        print("{}\t{}\t{}\t{}".format(account, item["account_email"], item["files"], state))


def build_parser():
    parser = argparse.ArgumentParser(description="CLIProxyAPI 多账号控制脚本")
    parser.add_argument("--root", default=str(DEFAULT_ROOT))
    sub = parser.add_subparsers(dest="command", required=True)

    key = sub.add_parser("key")
    key_sub = key.add_subparsers(dest="key_command", required=True)
    for name in ("create", "create-user", "revoke", "rotate"):
        child = key_sub.add_parser(name)
        child.add_argument("value")
        child.add_argument("--no-apply", action="store_true")
    create_users = key_sub.add_parser(
        "create-users",
        help="批量创建用户（去重、跳过已有）；默认不重建业务 CPA 容器",
    )
    create_users.add_argument(
        "emails",
        nargs="*",
        help="用户邮箱列表；也可通过 --file 或标准输入提供",
    )
    create_users.add_argument(
        "--file",
        "-f",
        help="邮箱文件，每行一个；传 - 表示从标准输入读取",
    )
    create_users.add_argument("--no-apply", action="store_true")
    create_users.add_argument(
        "--restart",
        action="store_true",
        help="apply 时强制重建全部业务 CPA（默认只渲染配置并热重载网关）",
    )
    create_users.add_argument("--dry-run", action="store_true")
    listing = key_sub.add_parser("list")
    listing.add_argument("--account")
    listing.add_argument("--user")

    stats = sub.add_parser("stats")
    stats_sub = stats.add_subparsers(dest="stats_command", required=True)
    current = stats_sub.add_parser("now")
    current.add_argument("--detail", action="store_true")
    active = stats_sub.add_parser("active")
    active.add_argument("--window", default="5m")
    active.add_argument("--account")
    active.add_argument("--detail", action="store_true")

    image = sub.add_parser("image")
    image_sub = image.add_subparsers(dest="image_command", required=True)
    image_sub.add_parser("status")
    image_sub.add_parser("pull")
    image_update = image_sub.add_parser("update")
    image_update.add_argument("target", nargs="?", default="all")

    for command in ("up", "stop", "restart", "logs"):
        child = sub.add_parser(command)
        child.add_argument("target", nargs="?", default="all")
    sub.add_parser("status")
    sub.add_parser("render")
    migrate = sub.add_parser("migrate-single-keys")
    migrate.add_argument("--no-apply", action="store_true")
    migrate.add_argument("--dry-run", action="store_true")
    rotate_legacy = sub.add_parser("rotate-legacy-keys")
    rotate_legacy.add_argument("--no-apply", action="store_true")
    rotate_legacy.add_argument("--dry-run", action="store_true")
    sub.add_parser("health")
    sub.add_parser("verify-routing")
    auth = sub.add_parser("auth")
    auth_sub = auth.add_subparsers(dest="auth_command", required=True)
    auth_sub.add_parser("status")
    login = sub.add_parser("login")
    login.add_argument("account")
    profile = sub.add_parser("profile", help="应用或查看 JSON 配置档案")
    profile_sub = profile.add_subparsers(dest="profile_command", required=True)
    profile_apply = profile_sub.add_parser("apply")
    profile_apply.add_argument("path", help="配置档案路径，传 - 从标准输入读取")
    profile_import = profile_sub.add_parser("import-once")
    profile_import.add_argument("path", help="配置档案路径，传 - 从标准输入读取")
    profile_import.add_argument(
        "--preserve-existing",
        action="store_true",
        help="仅校验并登记旧档案，不覆盖当前配置中心数据",
    )
    profile_sub.add_parser("show")
    store = sub.add_parser("store", help="验证或备份控制面数据库")
    store_sub = store.add_subparsers(dest="store_command", required=True)
    store_sub.add_parser("verify")
    store_backup = store_sub.add_parser("backup")
    store_backup.add_argument("path")
    store_migrate = store_sub.add_parser("migrate-secrets")
    store_migrate.add_argument("--cleanup", action="store_true")
    store_sub.add_parser(
        "cleanup-projections",
        help="验证 SQLite 后删除已废弃的控制面 JSON 投影",
    )
    for command in ("stage-deployment", "record-deployment"):
        deployment = sub.add_parser(command)
        deployment.add_argument("--version", required=True)
        deployment.add_argument("--commit", required=True)
        deployment.add_argument("--pipeline", required=True)
        deployment.add_argument("--deployed-at", required=True)
        deployment.add_argument("--metadata-image", required=True)
        deployment.add_argument("--admin-image", required=True)
        deployment.add_argument("--web-image", required=True)
        deployment.add_argument("--gateway-image", required=True)
        deployment.add_argument("--edge-image", required=True)
        deployment.add_argument("--gateway-port", required=True)
        deployment.add_argument("--gateway-internal-port", required=True)
        deployment.add_argument("--preserve-cliproxy-image", default="")
    return parser


def target_services(app, target):
    normalized = target.lower()
    services = app.services()
    if normalized == "all":
        # Keep the web control plane alive so a bulk stop can be recovered
        # from the interface instead of requiring terminal access.
        return app.runtime_services() + [
            "web", "management", "usage-collector", "log-maintenance",
        ]
    if normalized in services:
        return app.runtime_services_for_account(normalized)
    if normalized in (
        "web", "management", "usage-collector", "log-maintenance",
    ):
        return [normalized]
    raise ValueError(
        "目标必须是 all、web、management、"
        "usage-collector、log-maintenance 或有效账号标识"
    )


def main(argv=None):
    args = build_parser().parse_args(argv)
    app = ControlPlane(args.root)
    if not (args.command == "migrate-single-keys" and args.dry_run):
        app.ensure_layout()
    try:
        if args.command == "key":
            apply = not getattr(args, "no_apply", False)
            if args.key_command == "create":
                print_created([app.create_key(args.value, apply=apply)])
            elif args.key_command == "create-user":
                print_created(app.create_user(args.value, apply=apply))
            elif args.key_command == "create-users":
                emails = list(args.emails or [])
                if args.file:
                    if args.file == "-":
                        source = sys.stdin.read()
                    else:
                        source = Path(args.file).read_text(encoding="utf-8")
                    emails.extend(
                        line.strip()
                        for line in source.splitlines()
                        if line.strip() and not line.strip().startswith("#")
                    )
                if not emails:
                    raise ValueError("请提供邮箱参数、--file 或标准输入")
                result = app.create_users(
                    emails,
                    apply=apply,
                    restart_containers=bool(args.restart),
                    dry_run=bool(args.dry_run),
                )
                print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
                if result.get("users") and not result.get("dry_run"):
                    print_created(
                        [
                            {
                                "label": item["email"],
                                "account_email": "待用户选择账号",
                                "key": item["key"],
                            }
                            for item in result["users"]
                        ]
                    )
            elif args.key_command == "rotate":
                print_created([app.rotate_key(args.value, apply=apply)])
            elif args.key_command == "revoke":
                revoked = app.revoke_key(args.value, apply=apply)
                print("已吊销 {}".format(revoked["label"]))
            else:
                records = app._read_registry()
                if args.account:
                    if args.account not in app.accounts():
                        raise ValueError("未知账号：{}".format(args.account))
                    records = [item for item in records if item["account"] == args.account]
                if args.user:
                    user = app._normalize_user(args.user)
                    records = [item for item in records if item["user"] == user]
                print_records(sorted(records, key=lambda item: (item["label"], item["created_at"])))
        elif args.command == "render":
            app.render()
            app.compose("config", "--quiet")
            print("配置渲染和 Compose 校验通过")
        elif args.command == "migrate-single-keys":
            result = app.migrate_single_user_keys(
                apply=not args.no_apply,
                dry_run=args.dry_run,
            )
            print(json.dumps(result, ensure_ascii=False, sort_keys=True))
        elif args.command == "rotate-legacy-keys":
            result = app.rotate_legacy_keys(
                apply=not args.no_apply,
                dry_run=args.dry_run,
            )
            print(json.dumps(result, ensure_ascii=False, sort_keys=True))
        elif args.command == "stats":
            if args.stats_command == "now":
                print_stats(app.inflight_stats(), args.detail)
            else:
                stats = app.active_stats(parse_duration(args.window))
                if args.account:
                    if args.account not in stats:
                        raise ValueError("未知账号：{}".format(args.account))
                    stats = {
                        account: stats[account]
                        if account == args.account
                        else {
                            "account_email": app.accounts()[account]["email"],
                            "count": 0,
                            "labels": [],
                            "users": [],
                        }
                        for account in app.accounts()
                    }
                print_stats(stats, args.detail)
        elif args.command == "status":
            app.compose("ps")
        elif args.command == "image":
            if args.image_command == "status":
                print(json.dumps(app.cliproxy_image_status(), ensure_ascii=False, indent=2))
            elif args.image_command == "pull":
                app.pull_cliproxy_image()
            else:
                def interrupt_image_update(*unused):
                    raise ValueError("镜像更新已取消")

                previous_handler = signal.signal(signal.SIGTERM, interrupt_image_update)
                try:
                    app.update_cliproxy_image(args.target)
                finally:
                    signal.signal(signal.SIGTERM, previous_handler)
        elif args.command == "health":
            app.health()
        elif args.command == "verify-routing":
            print("LABEL\tACCOUNT\tHTTP")
            for item in app.verify_routing():
                print("{label}\t{account}\t{status}".format(**item))
        elif args.command == "auth":
            print_auth_status(app.auth_status())
        elif args.command == "login":
            app.login(args.account)
        elif args.command == "profile":
            if args.profile_command == "show":
                print(json.dumps(app.redacted_configuration(), ensure_ascii=False, indent=2, sort_keys=True))
            else:
                raw = sys.stdin.read() if args.path == "-" else Path(args.path).read_text(encoding="utf-8")
                payload = json.loads(raw)
                result = (
                    app.import_configuration_profile_once(
                        payload,
                        preserve_existing=bool(getattr(args, "preserve_existing", False)),
                    )
                    if args.profile_command == "import-once"
                    else app.apply_configuration_profile(payload)
                )
                profile_keys = set(payload.get("values", {}))
                if profile_keys & set(CONFIG_ENV_KEYS):
                    app.sync_environment_configuration(result["values"])
                print(json.dumps(app.redacted_configuration(result), ensure_ascii=False, indent=2, sort_keys=True))
        elif args.command == "store":
            if args.store_command == "backup":
                path = app.store.backup_to(args.path)
                print(str(path))
            elif args.store_command == "migrate-secrets":
                result = app.store.migrate_legacy_secrets(cleanup=args.cleanup)
                print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
            elif args.store_command == "cleanup-projections":
                result = app.store.cleanup_obsolete_projections()
                print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
            else:
                result = app.verify_control_plane_store()
                print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
                if not result["ok"]:
                    return 1
        elif args.command in ("stage-deployment", "record-deployment"):
            if not SEMANTIC_VERSION_RE.fullmatch(str(args.version)):
                raise ValueError("部署版本必须是语义化版本")
            metadata_image = app._normalize_configuration_value(
                CONFIG_DEFINITION_BY_KEY["delivery.release_metadata_image"],
                args.metadata_image,
            )
            payload = {
                "version": str(args.version),
                "commit": str(args.commit),
                "pipeline": str(args.pipeline),
                "deployed_at": str(args.deployed_at),
                "admin_image": str(args.admin_image),
                "web_image": str(args.web_image),
                "gateway_image": str(args.gateway_image),
                "edge_image": str(args.edge_image),
            }
            previous_deployment = app.store.read_runtime_state("deployment", None)
            previous_cliproxy_image = app.store.read_runtime_state(
                "cliproxy_image", None
            )
            previous_settings = app._read_stored_configuration()
            try:
                updated_settings = dict(previous_settings)
                updated_settings["delivery.release_metadata_image"] = metadata_image
                updated_settings["gateway.port"] = app._normalize_configuration_value(
                    CONFIG_DEFINITION_BY_KEY["gateway.port"], args.gateway_port
                )
                updated_settings["gateway.internal_port"] = app._normalize_configuration_value(
                    CONFIG_DEFINITION_BY_KEY["gateway.internal_port"],
                    args.gateway_internal_port,
                )
                effective = dict(app.configuration()["values"])
                effective.update(updated_settings)
                app._validate_configuration(effective)
                app._write_configuration(updated_settings)
                if args.preserve_cliproxy_image:
                    app.seed_cliproxy_applied_image(
                        args.preserve_cliproxy_image
                    )
                deployment_state = app.deployment_runtime_state()
                if args.command == "stage-deployment":
                    deployment_state["pending"] = payload
                else:
                    pending = deployment_state.get("pending")
                    if pending and pending != payload:
                        raise ValueError("已暂存部署与待验收部署信息不一致")
                    deployment_state["applied"] = payload
                    deployment_state.pop("pending", None)
                app.store.write_runtime_state("deployment", deployment_state)
                app.render_compose_environment()
            except BaseException:
                app._write_configuration(previous_settings)
                if previous_deployment is None:
                    app.store.delete_runtime_state("deployment")
                else:
                    app.store.write_runtime_state("deployment", previous_deployment)
                if previous_cliproxy_image is None:
                    app.store.delete_runtime_state("cliproxy_image")
                else:
                    app.store.write_runtime_state(
                        "cliproxy_image", previous_cliproxy_image
                    )
                try:
                    app.render_compose_environment()
                except Exception:
                    pass
                raise
            print(
                json.dumps(
                    {
                        **payload,
                        "status": (
                            "pending"
                            if args.command == "stage-deployment"
                            else "applied"
                        ),
                    },
                    ensure_ascii=False,
                    sort_keys=True,
                )
            )
        else:
            services = target_services(app, args.target)
            if args.command == "up":
                app.render()
                app.compose("config", "--quiet")
                app.compose("up", "-d", *services)
            elif args.command == "stop":
                app.compose("stop", *services)
            elif args.command == "restart":
                app.compose("restart", *services)
            elif args.command == "logs":
                app.compose("logs", "--tail", "200", *services)
    except (ValueError, subprocess.CalledProcessError, json.JSONDecodeError) as error:
        print("错误：{}".format(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
