#!/usr/bin/env python3
"""Scheduled enterprise WeChat markdown_v2 quota notifications."""

import hashlib
import json
import os
import re
import threading
import time
import urllib.parse
import urllib.request
from datetime import datetime, timedelta, timezone
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
import sys
sys.path.insert(0, str(PROJECT_ROOT / "scripts"))
from control_plane_store import ControlPlaneStore  # noqa: E402

try:
    from zoneinfo import ZoneInfo
except ImportError:  # Importing the admin module must remain possible on Python 3.8.
    ZoneInfo = None


STATE_VERSION = 1
MARKDOWN_V2_MAX_BYTES = 4096
WEBHOOK_HOST = "qyapi.weixin.qq.com"
WEBHOOK_PATH = "/cgi-bin/webhook/send"
WEBHOOK_RE = re.compile(
    r"https://qyapi\.weixin\.qq\.com/cgi-bin/webhook/send\?key=[^\s&\"']+",
    re.IGNORECASE,
)


def _timezone(name):
    if ZoneInfo is None:
        if name == "Asia/Shanghai":
            return timezone(timedelta(hours=8), name)
        if name in ("UTC", "Etc/UTC"):
            return timezone.utc
        raise RuntimeError("企业微信通知调度需要 Python 3.9 或更高版本")
    return ZoneInfo(name)


def redact_webhook(value):
    return WEBHOOK_RE.sub(
        "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=[REDACTED]",
        str(value),
    )


def validate_webhook_url(value):
    raw = str(value or "").strip()
    try:
        parsed = urllib.parse.urlsplit(raw)
        port = parsed.port
        query = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
    except ValueError:
        raise ValueError("Webhook 地址必须是企业微信消息推送 HTTPS 地址")
    keys = query.get("key", [])
    if (
        len(raw) > 2048
        or parsed.scheme != "https"
        or parsed.hostname != WEBHOOK_HOST
        or port is not None
        or parsed.path != WEBHOOK_PATH
        or parsed.username
        or parsed.password
        or parsed.fragment
        or set(query) != {"key"}
        or len(keys) != 1
        or not re.fullmatch(r"[A-Za-z0-9_-]{8,256}", keys[0])
    ):
        raise ValueError("Webhook 地址必须是企业微信消息推送 HTTPS 地址")
    return raw


def usage_center_url(public_base_url):
    """Build a Usage Center link without changing the configured public scheme."""
    raw = str(public_base_url or "").strip()
    if not raw:
        return ""
    return raw.rstrip("/") + "/usage/"


def _safe_cell(value, limit=48):
    text = str(value or "—").replace("|", "\\|").replace("\r", " ").replace("\n", " ").strip()
    if not text:
        return "—"
    return text if len(text) <= limit else text[: max(1, limit - 1)] + "…"


def _natural_key(value):
    return tuple(
        (1, int(part)) if part.isdigit() else (0, part.casefold())
        for part in re.split(r"(\d+)", str(value or ""))
    )


def _quota_row_sort_key(row):
    unavailable = row["level"] == "unavailable" or row["used_percent"] is None
    return (
        unavailable,
        0 if unavailable else float(row["used_percent"]),
        _natural_key(row["account"]),
        _natural_key(row["label"]),
    )


class WeComNotificationService:
    def __init__(self, root, opener=None, store=None):
        self.root = Path(root).resolve()
        self.webhook_path = self.root / "secrets" / "wecom-webhook.url"
        self.opener = opener or urllib.request.urlopen
        self.store = store or ControlPlaneStore(self.root)
        self.lock = threading.RLock()

    @staticmethod
    def _atomic_text(path, content, mode=0o600):
        path.parent.mkdir(parents=True, exist_ok=True)
        temporary = path.with_name(".{}.{}.tmp".format(path.name, os.getpid()))
        temporary.write_text(content, encoding="utf-8")
        os.chmod(temporary, mode)
        os.replace(temporary, path)
        os.chmod(path, mode)

    def webhook_configured(self):
        try:
            validate_webhook_url(self.store.read_secret("wecom_webhook") or "")
            return True
        except ValueError:
            return False

    def set_webhook(self, value):
        webhook = validate_webhook_url(value)
        self.store.write_secret("wecom_webhook", webhook)
        try:
            self.webhook_path.unlink()
        except OSError:
            pass
        return {"configured": True}

    def clear_webhook(self):
        self.store.delete_secret("wecom_webhook")
        try:
            self.webhook_path.unlink()
        except OSError:
            pass
        return {"configured": False}

    def _webhook_url(self):
        raw = self.store.read_secret("wecom_webhook")
        if not raw:
            raise ValueError("尚未配置企业微信 Webhook")
        return validate_webhook_url(raw)

    def webhook_url(self):
        try:
            return self._webhook_url()
        except ValueError:
            return ""

    def read_state(self):
        payload = self.store.read_runtime_state("notification")
        if payload is None:
            return {
                "version": STATE_VERSION,
                "scheduled": {},
                "quota_alerts": {},
                "quota_windows": {},
                "heartbeat_at": None,
                "last_success_at": None,
                "last_error": "",
                "next_schedule_at": None,
                "quota_checked_at": None,
            }
        if not isinstance(payload, dict) or payload.get("version") != STATE_VERSION:
            payload = {}
        payload.setdefault("version", STATE_VERSION)
        payload.setdefault("scheduled", {})
        payload.setdefault("quota_alerts", {})
        payload.setdefault("quota_windows", {})
        payload.setdefault("heartbeat_at", None)
        payload.setdefault("last_success_at", None)
        payload.setdefault("last_error", "")
        payload.setdefault("next_schedule_at", None)
        payload.setdefault("quota_checked_at", None)
        if not isinstance(payload["scheduled"], dict):
            payload["scheduled"] = {}
        if not isinstance(payload["quota_alerts"], dict):
            payload["quota_alerts"] = {}
        if not isinstance(payload["quota_windows"], dict):
            payload["quota_windows"] = {}
        return payload

    def write_state(self, state):
        state = dict(state)
        state["version"] = STATE_VERSION
        self.store.write_runtime_state("notification", state)

    def public_status(self):
        state = self.read_state()
        webhook_url = self.webhook_url()
        return {
            "webhook_configured": bool(webhook_url),
            "webhook_url": webhook_url,
            "heartbeat_at": state.get("heartbeat_at"),
            "last_success_at": state.get("last_success_at"),
            "last_error": redact_webhook(state.get("last_error", "")),
            "next_schedule_at": state.get("next_schedule_at"),
        }

    @staticmethod
    def quota_rows(snapshot, threshold_percent, only_keys=None):
        rows = []
        only_keys = set(only_keys or ())
        for account in snapshot.get("accounts", []):
            quota = account.get("quota") if isinstance(account.get("quota"), dict) else {}
            windows = quota.get("weekly_windows") if isinstance(quota.get("weekly_windows"), list) else []
            if not windows and isinstance(quota.get("weekly"), dict):
                windows = [{**quota["weekly"], "key": "default:primary_window", "label": "常规周限额"}]
            had_quota_windows = bool(windows)
            windows = [
                window
                for window in windows
                if "gpt-5.3" not in str(window.get("label") or "").casefold()
            ]
            usage = account.get("usage") if isinstance(account.get("usage"), dict) else {}
            reset_credits = quota.get("reset_credits") if isinstance(quota.get("reset_credits"), dict) else {}
            reset_count = reset_credits.get("available_count")
            if not isinstance(reset_count, int) or isinstance(reset_count, bool) or reset_count < 0:
                reset_count = None
            if not windows:
                if had_quota_windows:
                    continue
                key = "{}|unavailable".format(account.get("id", "unknown"))
                if only_keys and key not in only_keys:
                    continue
                rows.append(
                    {
                        "key": key,
                        "account": account.get("id", "unknown"),
                        "label": "常规周限额",
                        "used_percent": None,
                        "active_users": int(usage.get("active_users") or 0),
                        "reset_count": reset_count,
                        "reset_at": None,
                        "reset_key": None,
                        "level": "unavailable",
                    }
                )
                continue
            for window in windows:
                window_key = str(window.get("key") or "default:primary_window")
                key = "{}|{}".format(account.get("id", "unknown"), window_key)
                if only_keys and key not in only_keys:
                    continue
                try:
                    used = max(0.0, min(float(window.get("used_percent")), 100.0))
                except (TypeError, ValueError):
                    used = None
                if quota.get("status") != "ok" or used is None:
                    level = "unavailable"
                elif window.get("limit_reached") is True or used >= 100:
                    level = "exhausted"
                elif used >= float(threshold_percent):
                    level = "warning"
                else:
                    level = "normal"
                rows.append(
                    {
                        "key": key,
                        "account": account.get("id", "unknown"),
                        "label": window.get("label") or "常规周限额",
                        "used_percent": used,
                        "active_users": int(usage.get("active_users") or 0),
                        "reset_count": reset_count,
                        "reset_at": window.get("reset_at"),
                        "reset_key": window.get("reset_at"),
                        "level": level,
                    }
                )
        rows.sort(key=_quota_row_sort_key)
        return rows

    @staticmethod
    def _format_percent(value):
        if value is None:
            return "—"
        rendered = "{:.2f}".format(float(value)).rstrip("0").rstrip(".")
        return rendered + "%"

    @staticmethod
    def _format_reset(timestamp, timezone_name, now_epoch):
        try:
            timestamp = int(timestamp)
        except (TypeError, ValueError):
            return "—"
        if timestamp <= 0:
            return "—"
        zone = _timezone(timezone_name)
        if timestamp <= int(now_epoch):
            return "等待刷新"
        return datetime.fromtimestamp(timestamp, zone).strftime("%m-%d %H:%M")

    def build_markdown_v2(
        self,
        snapshot,
        title,
        timezone_name,
        threshold_percent,
        now_epoch=None,
        only_keys=None,
        transition_events=None,
        usage_center_url="",
    ):
        now_epoch = int(time.time()) if now_epoch is None else int(now_epoch)
        zone = _timezone(timezone_name)
        generated = datetime.fromtimestamp(now_epoch, zone).strftime("%Y-%m-%d %H:%M")
        rows = self.quota_rows(snapshot, threshold_percent, only_keys=only_keys)
        transition_events = {
            str(key): str(value)
            for key, value in (transition_events or {}).items()
        }
        transition_labels = {
            "warning": "🟠 达到预警",
            "exhausted": "🔴 额度耗尽",
            "recovered": "🟢 额度恢复",
            "recovered_warning": "🟠 恢复至预警",
            "refreshed": "🔄 额度刷新",
        }
        icons = {
            "normal": "🟢",
            "warning": "🟠",
            "exhausted": "🔴",
            "unavailable": "⚪",
        }
        if transition_events:
            table = [
                "| 事件 | CPA账号 / 额度窗口 | 已用 | 1h用户 | 重置次数 | 下次刷新 |",
                "| :--- | :--- | ---: | ---: | ---: | :--- |",
            ]
        else:
            table = [
                "| CPA账号 / 额度窗口 | 已用 | 1h用户 | 重置次数 | 下次刷新 |",
                "| :--- | ---: | ---: | ---: | :--- |",
            ]
        for row in rows:
            name = "{} {} · {}".format(
                icons[row["level"]],
                _safe_cell(row["account"], 32),
                _safe_cell(row["label"], 24),
            )
            cells = [
                name,
                self._format_percent(row["used_percent"]),
                row["active_users"],
                row["reset_count"] if row["reset_count"] is not None else "—",
                self._format_reset(row["reset_at"], timezone_name, now_epoch),
            ]
            if transition_events:
                cells.insert(
                    0,
                    transition_labels.get(transition_events.get(row["key"]), "—"),
                )
            table.append("| {} |".format(" | ".join(str(cell) for cell in cells)))
        if not rows:
            if transition_events:
                table.append("| — | ⚪ 暂无匹配账号 | — | 0 | — | — |")
            else:
                table.append("| ⚪ 暂无匹配账号 | — | 0 | — | — |")
        legend = "> 🟢 正常　🟠 超过阈值　🔴 额度耗尽　⚪ 数据不可用"
        if transition_events:
            legend += "　🔄 额度刷新"
        sections = [
                "# {}".format(_safe_cell(title, 64)),
                "> 统计时间：{}　预警阈值：{}%".format(
                    generated,
                    self._format_percent(threshold_percent).rstrip("%"),
                ),
        ]
        usage_center_url = str(usage_center_url or "").strip()
        if usage_center_url:
            sections.append("> 应用地址：[{}]({})".format(usage_center_url, usage_center_url))
        sections.extend(("\n".join(table), legend))
        content = "\n\n".join(sections)
        if len(content.encode("utf-8")) > MARKDOWN_V2_MAX_BYTES:
            raise ValueError("企业微信 markdown_v2 内容超过 4096 字节")
        return content

    def send_content(self, content, timeout=10):
        if len(str(content).encode("utf-8")) > MARKDOWN_V2_MAX_BYTES:
            raise ValueError("企业微信 markdown_v2 内容超过 4096 字节")
        webhook = self._webhook_url()
        data = json.dumps(
            {
                "msgtype": "markdown_v2",
                "markdown_v2": {"content": str(content)},
            },
            ensure_ascii=False,
            separators=(",", ":"),
        ).encode("utf-8")
        request = urllib.request.Request(
            webhook,
            data=data,
            headers={"Content-Type": "application/json; charset=utf-8"},
            method="POST",
        )
        try:
            with self.opener(request, timeout=timeout) as response:
                payload = json.loads(response.read().decode("utf-8"))
        except Exception as error:
            raise RuntimeError("企业微信消息发送失败：{}".format(redact_webhook(error)))
        if not isinstance(payload, dict) or payload.get("errcode") != 0:
            raise RuntimeError(
                "企业微信消息发送失败：{}".format(
                    redact_webhook(payload.get("errmsg", "响应无效") if isinstance(payload, dict) else "响应无效")
                )
            )
        return {"errcode": 0, "errmsg": str(payload.get("errmsg") or "ok")}

    @staticmethod
    def payload_hash(content):
        return hashlib.sha256(str(content).encode("utf-8")).hexdigest()


class NotificationScheduler:
    def __init__(self, app, interval_seconds=30):
        self.app = app
        self.service = app.notifications
        self.interval_seconds = max(5, int(interval_seconds))
        self.stopping = threading.Event()
        self.thread = None

    def start(self):
        if self.thread and self.thread.is_alive():
            return
        self.thread = threading.Thread(
            target=self._run,
            name="wecom-notification-scheduler",
            daemon=True,
        )
        self.thread.start()

    def stop(self):
        self.stopping.set()
        if self.thread:
            self.thread.join(timeout=5)

    @staticmethod
    def _schedule_times(raw):
        return [item for item in str(raw or "").split(",") if item]

    @classmethod
    def _next_schedule_at(cls, now_local, raw_times):
        candidates = []
        for day_offset in (0, 1):
            date = (now_local + timedelta(days=day_offset)).date()
            for item in cls._schedule_times(raw_times):
                hour, minute = (int(part) for part in item.split(":", 1))
                candidate = datetime(
                    date.year,
                    date.month,
                    date.day,
                    hour,
                    minute,
                    tzinfo=now_local.tzinfo,
                )
                if candidate > now_local:
                    candidates.append(candidate)
        return int(min(candidates).timestamp()) if candidates else None

    @staticmethod
    def _prune_scheduled(scheduled, now_epoch):
        cutoff = int(now_epoch) - 14 * 24 * 60 * 60
        retained = {}
        for key, value in (scheduled.items() if isinstance(scheduled, dict) else ()):
            try:
                sent_at = int(value.get("sent_at") or 0) if isinstance(value, dict) else 0
            except (TypeError, ValueError):
                sent_at = 0
            if sent_at >= cutoff:
                retained[key] = value
        return retained

    def _send_snapshot(
        self,
        snapshot,
        title,
        config,
        now_epoch,
        only_keys=None,
        transition_events=None,
    ):
        content = self.service.build_markdown_v2(
            snapshot,
            title=title,
            timezone_name=config["notification.timezone"],
            threshold_percent=config["notification.weekly_threshold_percent"],
            now_epoch=now_epoch,
            only_keys=only_keys,
            transition_events=transition_events,
            usage_center_url=usage_center_url(config.get("branding.public_base_url")),
        )
        with self.service.lock:
            self.service.send_content(content)
        return content

    def run_once(self, now_epoch=None):
        now_epoch = int(time.time()) if now_epoch is None else int(now_epoch)
        config = self.app.control.configuration()["values"]
        state = self.service.read_state()
        state["heartbeat_at"] = now_epoch
        state["scheduled"] = self._prune_scheduled(state.get("scheduled", {}), now_epoch)
        if not config["notification.enabled"] or not self.service.webhook_configured():
            state["next_schedule_at"] = None
            self.service.write_state(state)
            return {"sent": [], "enabled": False}

        zone = _timezone(config["notification.timezone"])
        now_local = datetime.fromtimestamp(now_epoch, zone)
        state["next_schedule_at"] = self._next_schedule_at(
            now_local,
            config["notification.daily_times"],
        )

        try:
            due_keys = []
            grace = max(60, int(config["notification.schedule_grace_minutes"]) * 60)
            for item in self._schedule_times(config["notification.daily_times"]):
                hour, minute = (int(part) for part in item.split(":", 1))
                due = datetime(
                    now_local.year,
                    now_local.month,
                    now_local.day,
                    hour,
                    minute,
                    tzinfo=zone,
                )
                key = "{}|{}@{}".format(
                    config["notification.timezone"],
                    now_local.date().isoformat(),
                    item,
                )
                if due.timestamp() <= now_epoch < due.timestamp() + grace and key not in state["scheduled"]:
                    due_keys.append(key)

            quota_alert_enabled = bool(config["notification.quota_alert_enabled"])
            quota_check_interval = max(60, int(config["usage.quota_cache_seconds"]))
            last_quota_check = int(state.get("quota_checked_at") or 0)
            quota_check_due = quota_alert_enabled and (
                not last_quota_check or now_epoch >= last_quota_check + quota_check_interval
            )
            if not due_keys and not quota_check_due:
                self.service.write_state(state)
                return {"sent": [], "enabled": True}

            snapshot = self.app.account_management(3600)
            threshold = config["notification.weekly_threshold_percent"]
            current_signals = {}
            current_accounts = {
                str(account.get("id", "unknown"))
                for account in snapshot.get("accounts", [])
                if isinstance(account, dict)
            }
            previous_alerts = {
                str(key): value
                for key, value in state.get("quota_alerts", {}).items()
                if isinstance(value, dict) and value.get("alerted_at")
            }
            previous_windows = {
                str(key): value
                for key, value in state.get("quota_windows", {}).items()
                if (
                    isinstance(value, dict)
                    and isinstance(value.get("used_percent"), (int, float))
                    and not isinstance(value.get("used_percent"), bool)
                )
            }
            transition_events = {}
            evaluate_alerts = quota_alert_enabled and (quota_check_due or bool(due_keys))
            if evaluate_alerts:
                rows = self.service.quota_rows(snapshot, threshold)
                current_signals = {row["key"]: row for row in rows}
                for key, row in current_signals.items():
                    previous_level = str(previous_alerts.get(key, {}).get("level") or "")
                    if row["level"] == "warning":
                        if previous_level == "exhausted":
                            transition_events[key] = "recovered_warning"
                        elif previous_level not in ("warning", "exhausted"):
                            transition_events[key] = "warning"
                    elif row["level"] == "exhausted":
                        if previous_level != "exhausted":
                            transition_events[key] = "exhausted"
                    elif row["level"] == "normal" and previous_level in (
                        "warning",
                        "exhausted",
                    ):
                        transition_events[key] = "recovered"
                    previous_window = previous_windows.get(key)
                    if (
                        row["level"] != "unavailable"
                        and previous_window
                        and row["used_percent"] < previous_window["used_percent"]
                        and row["used_percent"] < 10
                    ):
                        transition_events[key] = "refreshed"

                # Every valid observation becomes the next comparison baseline.
                # Persist it even if a following notification delivery fails.
                updated_windows = {
                    key: value
                    for key, value in previous_windows.items()
                    if key.split("|", 1)[0] in current_accounts
                }
                for key, row in current_signals.items():
                    if row["level"] == "unavailable":
                        continue
                    updated_windows[key] = {
                        "used_percent": row["used_percent"],
                        "observed_at": now_epoch,
                    }
                state["quota_windows"] = updated_windows
                state["quota_checked_at"] = now_epoch

            sent = []
            scheduled_sent = False
            if due_keys:
                content = self._send_snapshot(
                    snapshot,
                    "{} · 账号额度报告".format(config["branding.short_name"]),
                    config,
                    now_epoch,
                    transition_events=transition_events,
                )
                payload_hash = self.service.payload_hash(content)
                for due_key in due_keys:
                    state["scheduled"][due_key] = {
                        "sent_at": now_epoch,
                        "payload_hash": payload_hash,
                    }
                sent.append("scheduled")
                scheduled_sent = True

            if transition_events and not scheduled_sent:
                event_types = set(transition_events.values())
                if event_types == {"warning"}:
                    title = "CPA 周额度预警"
                    sent_label = "quota_alert"
                elif event_types == {"exhausted"}:
                    title = "CPA 周额度耗尽"
                    sent_label = "quota_exhausted"
                elif event_types <= {"recovered", "recovered_warning"}:
                    title = "CPA 额度恢复"
                    sent_label = "quota_recovered"
                elif event_types == {"refreshed"}:
                    title = "{} · 账号额度刷新".format(config["branding.short_name"])
                    sent_label = "quota_refreshed"
                else:
                    title = "CPA 额度状态变更"
                    sent_label = "quota_transition"
                content = self._send_snapshot(
                    snapshot,
                    title,
                    config,
                    now_epoch,
                    only_keys=transition_events,
                    transition_events=transition_events,
                )
                sent.append(sent_label)

            if evaluate_alerts:
                updated_alerts = {
                    key: value
                    for key, value in previous_alerts.items()
                    if key.split("|", 1)[0] in current_accounts
                }
                for key, row in current_signals.items():
                    if row["level"] == "normal":
                        updated_alerts.pop(key, None)
                    elif row["level"] in ("warning", "exhausted"):
                        previous = previous_alerts.get(key)
                        if previous:
                            updated_alerts[key] = {
                                **previous,
                                "reset_at": row.get("reset_key"),
                                "level": row["level"],
                                "threshold": threshold,
                                "transitioned_at": (
                                    now_epoch
                                    if key in transition_events
                                    else previous.get("transitioned_at")
                                ),
                            }
                        elif key in transition_events and (
                            scheduled_sent or bool(sent)
                        ):
                            updated_alerts[key] = {
                                "reset_at": row.get("reset_key"),
                                "level": row["level"],
                                "threshold": threshold,
                                "alerted_at": now_epoch,
                                "transitioned_at": now_epoch,
                            }
                state["quota_alerts"] = updated_alerts
            if sent:
                state["last_success_at"] = now_epoch
            state["last_error"] = ""
            self.service.write_state(state)
            return {"sent": sent, "enabled": True}
        except Exception as error:
            state["last_error"] = redact_webhook(error)
            self.service.write_state(state)
            raise

    def _run(self):
        print("CPA enterprise WeChat notification scheduler started", flush=True)
        while not self.stopping.is_set():
            try:
                self.run_once()
            except Exception as error:
                print(
                    "notification scheduler failed: {}: {}".format(
                        type(error).__name__,
                        redact_webhook(error),
                    ),
                    flush=True,
                )
            self.stopping.wait(self.interval_seconds)
