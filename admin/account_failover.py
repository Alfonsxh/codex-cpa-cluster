#!/usr/bin/env python3
"""Observe official CPA quotas and atomically evacuate exhausted account routes."""

import json
import os
import sys
import threading
import time
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(PROJECT_ROOT / "scripts"))
from control_plane_store import ControlPlaneStore  # noqa: E402


STATE_VERSION = 1
MODES = {"off", "observe", "active"}


def _number(value):
    if isinstance(value, bool):
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


def _default_weekly(quota):
    weekly = quota.get("weekly") if isinstance(quota.get("weekly"), dict) else None
    if weekly and (
        not weekly.get("key") or str(weekly.get("key")).startswith("default:")
    ):
        return weekly
    for window in quota.get("weekly_windows", []):
        if (
            isinstance(window, dict)
            and str(window.get("key") or "").startswith("default:")
        ):
            return window
    return None


def build_account_states(
    accounts,
    quota_payload,
    services,
    service_names,
    auth,
    now,
    stale_after_seconds,
    reserve_percent,
):
    """Normalize routing eligibility without treating unknown quota as capacity."""
    now = int(now)
    observed_at = int(quota_payload.get("generated_at") or 0)
    fresh = observed_at > 0 and now - observed_at <= int(stale_after_seconds)
    quotas = {
        item.get("account"): item
        for item in quota_payload.get("accounts", [])
        if isinstance(item, dict) and item.get("account")
    }
    output = {}
    for account, metadata in accounts.items():
        quota = quotas.get(account, {})
        weekly = _default_weekly(quota)
        used = _number(weekly.get("used_percent")) if weekly else None
        remaining = _number(weekly.get("remaining_percent")) if weekly else None
        exhausted = bool(
            fresh
            and quota.get("status") == "ok"
            and (
                quota.get("limit_reached") is True
                or (weekly and weekly.get("limit_reached") is True)
            )
        )
        enabled = metadata.get("group_enabled") is not False
        service = services.get(service_names.get(account, ""), {})
        running = str(service.get("state") or "").lower() == "running"
        oauth_configured = int(auth.get(account, {}).get("files") or 0) > 0
        headroom = (
            max(0.0, remaining - float(reserve_percent))
            if remaining is not None
            else 0.0
        )
        eligible = bool(
            enabled
            and running
            and oauth_configured
            and fresh
            and quota.get("status") == "ok"
            and weekly is not None
            and quota.get("allowed") is not False
            and not exhausted
            and headroom > 0
        )
        if exhausted:
            reason = "quota_exhausted"
        elif not enabled:
            reason = "account_disabled"
        elif not running:
            reason = "container_not_running"
        elif not oauth_configured:
            reason = "oauth_missing"
        elif not fresh:
            reason = "quota_stale"
        elif quota.get("status") != "ok" or weekly is None:
            reason = "quota_unavailable"
        elif quota.get("allowed") is False:
            reason = "upstream_disallowed"
        elif headroom <= 0:
            reason = "reserve_reached"
        else:
            reason = "available"
        output[account] = {
            "account": account,
            "eligible": eligible,
            "exhausted": exhausted,
            "reason": reason,
            "used_percent": used,
            "remaining_percent": remaining,
            "headroom": headroom,
            "reset_at": int(weekly.get("reset_at") or 0) if weekly else 0,
            "observed_at": observed_at,
        }
    return output


def plan_failover(
    routes,
    active_users,
    routable_users,
    account_states,
    source_accounts=None,
):
    """Use weighted least-routed allocation, with official remaining quota as weight."""
    active_users = set(active_users)
    routable_users = set(routable_users)
    source_accounts = (
        {
            account
            for account, state in account_states.items()
            if state["exhausted"]
        }
        if source_accounts is None
        else set(source_accounts)
    )
    candidates = sorted(
        account
        for account, state in account_states.items()
        if state["eligible"] and account not in source_accounts
    )
    affected = sorted(
        (user, account)
        for user, account in routes.items()
        if user in active_users and account in source_accounts
    )
    source_counts = {}
    for _, account in affected:
        source_counts[account] = source_counts.get(account, 0) + 1

    routed_counts = {account: 0 for account in candidates}
    for user, account in routes.items():
        if user in active_users and account in routed_counts:
            routed_counts[account] += 1

    assignments = {}
    expected_routes = {}
    destinations = {}
    skipped_users = 0
    for user, source in affected:
        if user not in routable_users:
            skipped_users += 1
            continue
        if not candidates:
            continue
        target = min(
            candidates,
            key=lambda account: (
                (routed_counts[account] + 1) / account_states[account]["headroom"],
                account_states[account]["used_percent"]
                if account_states[account]["used_percent"] is not None
                else 101.0,
                account,
            ),
        )
        assignments[user] = target
        expected_routes[user] = source
        routed_counts[target] += 1
        destinations[target] = destinations.get(target, 0) + 1

    return {
        "assignments": assignments,
        "expected_routes": expected_routes,
        "sources": dict(sorted(source_counts.items())),
        "destinations": dict(sorted(destinations.items())),
        "candidate_accounts": candidates,
        "affected_users": len(affected),
        "planned_users": len(assignments),
        "skipped_users": skipped_users,
        "unassigned_users": len(affected) - len(assignments),
    }


def plan_summary(plan):
    return {
        key: plan[key]
        for key in (
            "sources",
            "destinations",
            "candidate_accounts",
            "affected_users",
            "planned_users",
            "skipped_users",
            "unassigned_users",
        )
    }


class AccountFailoverService:
    def __init__(self, root, store=None):
        self.root = Path(root).resolve()
        self.store = store or ControlPlaneStore(self.root)
        self.lock = threading.RLock()

    @staticmethod
    def _default_state():
        return {
            "version": STATE_VERSION,
            "mode": "off",
            "heartbeat_at": None,
            "last_check_at": None,
            "last_success_at": None,
            "next_check_at": None,
            "quota_refreshing": False,
            "last_error": "",
            "accounts": {},
            "last_plan": None,
            "last_action": None,
        }

    def read_state(self):
        state = self._default_state()
        payload = self.store.read_runtime_state("account_failover")
        if payload is None:
            return state
        if not isinstance(payload, dict) or payload.get("version") != STATE_VERSION:
            return state
        state.update(payload)
        if state.get("mode") not in MODES:
            state["mode"] = "off"
        if not isinstance(state.get("accounts"), dict):
            state["accounts"] = {}
        return state

    def write_state(self, state):
        payload = {**self._default_state(), **dict(state), "version": STATE_VERSION}
        self.store.write_runtime_state("account_failover", payload)
        return payload

    def public_status(self):
        return self.read_state()

    @staticmethod
    def _routable_users(records, accounts):
        active_users = {}
        for record in records:
            if record.get("status") == "active":
                active_users.setdefault(record.get("user"), []).append(record)
        routable = set()
        for user, items in active_users.items():
            if (
                user
                and len({item.get("key") for item in items}) == 1
                and set(accounts).issubset({item.get("account") for item in items})
            ):
                routable.add(user)
        return set(active_users), routable

    def _current_plan(
        self,
        app,
        quota_payload,
        config,
        now,
        source_accounts=None,
    ):
        accounts = app.control.accounts()
        services = {
            item.get("service"): item
            for item in app._compose_ps()
            if item.get("service")
        }
        account_states = build_account_states(
            accounts,
            quota_payload,
            services,
            app.control.services(),
            app.control.auth_status(),
            now,
            config["account_failover.stale_after_seconds"],
            config["account_failover.reserve_percent"],
        )
        records = app.control._read_registry()
        active_users, routable_users = self._routable_users(records, accounts)
        plan = plan_failover(
            app.control._read_routes(),
            active_users,
            routable_users,
            account_states,
            source_accounts=source_accounts,
        )
        return account_states, plan

    def rebalance_account(self, app, account, now=None):
        """Manually evacuate one account while reusing automatic target eligibility and weights."""
        now = int(time.time()) if now is None else int(now)
        account = app.control._normalize_account_id(account)
        config = app.control.configuration()["values"]
        poll_seconds = int(config["account_failover.poll_seconds"])
        with app.action_lock:
            accounts = app.control.accounts()
            if account not in accounts:
                raise ValueError("业务 CPA 不存在：{}".format(account))
            if not any(
                target != account and metadata.get("group_enabled") is not False
                for target, metadata in accounts.items()
            ):
                raise ValueError("当前没有其他已启用的目标 CPA")

            records = app.control._read_registry()
            active_users, routable_users = self._routable_users(records, accounts)
            routes = app.control._read_routes()
            affected_users = {
                user
                for user, source in routes.items()
                if source == account and user in active_users
            }
            if not affected_users:
                raise ValueError("当前账号没有需要迁移的有效用户")
            skipped_users = affected_users - routable_users
            if skipped_users:
                raise ValueError(
                    "有 {} 位用户尚未完成统一 Key 迁移，已停止本次操作".format(
                        len(skipped_users)
                    )
                )

            quota_payload = app.refresh_usage_limits_sync()
            account_states, plan = self._current_plan(
                app,
                quota_payload,
                config,
                now,
                source_accounts={account},
            )
            if not plan["assignments"] or plan["unassigned_users"]:
                raise ValueError("当前没有满足额度、OAuth 和运行状态条件的目标 CPA")
            result = app.control.set_user_routes(
                plan["assignments"],
                expected_routes=plan["expected_routes"],
                apply=True,
                wait_for_gateway=True,
            )

        summary = plan_summary(plan)
        with self.lock:
            state = self.read_state()
            state.update(
                {
                    "mode": config["account_failover.mode"],
                    "heartbeat_at": now,
                    "last_check_at": now,
                    "last_success_at": now,
                    "next_check_at": now + poll_seconds,
                    "quota_refreshing": False,
                    "last_error": "",
                    "accounts": account_states,
                    "last_plan": summary,
                    "last_action": {
                        "at": now,
                        "trigger": "manual",
                        "sources": summary["sources"],
                        "destinations": result["destinations"],
                        "moved_users": result["moved_users"],
                        "snapshot_generation": (
                            (result.get("snapshot") or {}).get("generation") or ""
                        ),
                    },
                }
            )
            self.write_state(state)
        audit_target = "{} -> {}".format(
            account,
            ",".join(
                "{}:{}".format(target, count)
                for target, count in result["destinations"].items()
            ),
        )
        app.audit("account.failover.manual_rebalance", audit_target)
        return {
            "account": account,
            "moved_users": result["moved_users"],
            "destinations": result["destinations"],
            "quota_generated_at": int(quota_payload.get("generated_at") or 0),
            "snapshot_generation": (
                (result.get("snapshot") or {}).get("generation") or ""
            ),
        }

    def run_once(self, app, now=None):
        now = int(time.time()) if now is None else int(now)
        config = app.control.configuration()["values"]
        mode = config["account_failover.mode"]
        poll_seconds = int(config["account_failover.poll_seconds"])
        with self.lock:
            state = self.read_state()
            previous_mode = state.get("mode")
            previous_heartbeat = int(state.get("heartbeat_at") or 0)
            state["mode"] = mode
            state["heartbeat_at"] = now
            if mode == "off":
                state["next_check_at"] = None
                state["quota_refreshing"] = False
                state["last_error"] = ""
                if (
                    not previous_heartbeat
                    or previous_mode != mode
                    or now - previous_heartbeat >= 60
                ):
                    self.write_state(state)
                return {"mode": mode, "checked": False, "moved_users": 0}
            last_check_at = int(state.get("last_check_at") or 0)
            if (
                previous_mode == mode
                and last_check_at
                and now < last_check_at + poll_seconds
            ):
                return {"mode": mode, "checked": False, "moved_users": 0}

        quota_payload = app.usage_limits(force_refresh=True)
        if quota_payload.get("refreshing"):
            with self.lock:
                state = self.read_state()
                state.update(
                    {
                        "mode": mode,
                        "heartbeat_at": now,
                        "next_check_at": now + 5,
                        "quota_refreshing": True,
                        "last_error": "",
                    }
                )
                self.write_state(state)
            return {"mode": mode, "checked": False, "refreshing": True, "moved_users": 0}

        account_states, plan = self._current_plan(app, quota_payload, config, now)
        result = None
        rebalance_audit_target = None
        if mode == "active" and plan["assignments"]:
            with app.action_lock:
                # A user or administrator may have changed routes while quota data was fetched.
                account_states, plan = self._current_plan(app, quota_payload, config, now)
                if plan["assignments"]:
                    result = app.control.set_user_routes(
                        plan["assignments"],
                        expected_routes=plan["expected_routes"],
                        apply=True,
                        wait_for_gateway=True,
                    )
                    rebalance_audit_target = "{} -> {}".format(
                        ",".join(plan["sources"]),
                        ",".join(
                            "{}:{}".format(account, count)
                            for account, count in result["destinations"].items()
                        ),
                    )

        summary = plan_summary(plan)
        capacity_alert = False
        with self.lock:
            state = self.read_state()
            capacity_alert = bool(
                summary["unassigned_users"]
                and state.get("last_plan") != summary
            )
            state.update(
                {
                    "mode": mode,
                    "heartbeat_at": now,
                    "last_check_at": now,
                    "last_success_at": now,
                    "next_check_at": now + poll_seconds,
                    "quota_refreshing": False,
                    "last_error": "",
                    "accounts": account_states,
                    "last_plan": summary,
                }
            )
            if result and result["moved_users"]:
                state["last_action"] = {
                    "at": now,
                    "trigger": "automatic",
                    "sources": summary["sources"],
                    "destinations": result["destinations"],
                    "moved_users": result["moved_users"],
                    "snapshot_generation": (
                        (result.get("snapshot") or {}).get("generation") or ""
                    ),
                }
            self.write_state(state)
        if rebalance_audit_target:
            app.audit("account.failover.rebalance", rebalance_audit_target)
        if capacity_alert:
            app.audit(
                "account.failover.capacity_unavailable",
                "{} unassigned:{}".format(
                    ",".join(summary["sources"]),
                    summary["unassigned_users"],
                ),
            )
        return {
            "mode": mode,
            "checked": True,
            "moved_users": result["moved_users"] if result else 0,
            "plan": summary,
        }

    def record_error(self, error, now=None):
        now = int(time.time()) if now is None else int(now)
        with self.lock:
            state = self.read_state()
            state["heartbeat_at"] = now
            state["quota_refreshing"] = False
            state["last_error"] = "{}: {}".format(
                type(error).__name__, str(error)
            )[:500]
            state["next_check_at"] = now + 5
            self.write_state(state)


class AccountFailoverScheduler:
    def __init__(self, app, interval_seconds=5):
        self.app = app
        self.service = app.account_failover
        self.interval_seconds = max(1, int(interval_seconds))
        self.stopping = threading.Event()
        self.thread = None

    def start(self):
        if self.thread and self.thread.is_alive():
            return
        self.thread = threading.Thread(
            target=self._run,
            name="account-failover-scheduler",
            daemon=True,
        )
        self.thread.start()

    def stop(self):
        self.stopping.set()
        if self.thread:
            self.thread.join(timeout=5)

    def _run(self):
        print("CPA account failover scheduler started", flush=True)
        while not self.stopping.is_set():
            try:
                self.service.run_once(self.app)
            except Exception as error:
                self.service.record_error(error)
                print(
                    "account failover scheduler failed: {}: {}".format(
                        type(error).__name__, error
                    ),
                    file=sys.stderr,
                    flush=True,
                )
            self.stopping.wait(self.interval_seconds)
