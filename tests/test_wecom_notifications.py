import importlib.util
import json
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).parents[1] / "admin" / "wecom_notifications.py"


def load_module():
    spec = importlib.util.spec_from_file_location("wecom_notifications_test", MODULE_PATH)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


MODULE = load_module()
WEBHOOK = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-placeholder"


def snapshot(used=25, reset_at=1784618552, second_window=False, account="alpha"):
    windows = [
        {
            "key": "default:primary_window",
            "label": "常规周限额",
            "used_percent": used,
            "reset_at": reset_at,
            "limit_reached": used >= 100,
        }
    ]
    if second_window:
        windows.append(
            {
                "key": "default:secondary_window",
                "label": "第二周限额",
                "used_percent": 40,
                "reset_at": reset_at + 3600,
                "limit_reached": False,
            }
        )
    return {
        "accounts": [
            {
                "id": account,
                "usage": {"active_users": 3},
                "quota": {
                    "status": "ok",
                    "weekly_windows": windows,
                    "reset_credits": {"available_count": 2},
                },
            }
        ]
    }


class FakeResponse:
    def __init__(self, payload):
        self.payload = payload

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False

    def read(self):
        return json.dumps(self.payload).encode("utf-8")


class WeComNotificationServiceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)

    def tearDown(self):
        self.tmp.cleanup()

    def test_webhook_validation_storage_and_redaction(self):
        service = MODULE.WeComNotificationService(self.root)

        for invalid in (
            "http://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-placeholder",
            "https://example.com/cgi-bin/webhook/send?key=test-placeholder",
            "https://qyapi.weixin.qq.com/cgi-bin/webhook/send",
            "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-placeholder&debug=1",
        ):
            with self.assertRaisesRegex(ValueError, "企业微信"):
                service.set_webhook(invalid)

        service.set_webhook(WEBHOOK)
        self.assertTrue(service.webhook_configured())
        self.assertFalse(service.webhook_path.exists())
        self.assertEqual(service.store.read_secret("wecom_webhook"), WEBHOOK)
        self.assertEqual(service.public_status()["webhook_url"], WEBHOOK)
        self.assertEqual(
            MODULE.redact_webhook("failed " + WEBHOOK),
            "failed https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=[REDACTED]",
        )

        service.store.write_secret("wecom_webhook", "https://example.com/?key=test-placeholder")
        self.assertFalse(service.webhook_configured())

    @unittest.skipIf(MODULE.ZoneInfo is None, "zoneinfo requires Python 3.9+")
    def test_markdown_v2_table_contains_all_required_fields_and_windows(self):
        service = MODULE.WeComNotificationService(self.root)
        content = service.build_markdown_v2(
            snapshot(90, second_window=True),
            title="CPA 账号额度报告",
            timezone_name="Asia/Shanghai",
            threshold_percent=90,
            now_epoch=1784600000,
            usage_center_url=MODULE.usage_center_url("http://cpa.example.com"),
        )

        self.assertIn(
            "| CPA账号 / 额度窗口 | 已用 | 1h用户 | 重置次数 | 下次刷新 |",
            content,
        )
        self.assertIn(
            "> 应用地址：[http://cpa.example.com/usage/]"
            "(http://cpa.example.com/usage/)",
            content,
        )
        self.assertIn("🟠 alpha · 常规周限额", content)
        self.assertIn("90% | 3 | 2", content)
        self.assertIn("🟢 alpha · 第二周限额", content)
        self.assertEqual(content.count("alpha"), 2)

    def test_usage_center_url_preserves_configured_http_or_https_scheme(self):
        self.assertEqual(
            MODULE.usage_center_url("http://cpa.example.com"),
            "http://cpa.example.com/usage/",
        )
        self.assertEqual(
            MODULE.usage_center_url("https://cpa.example.com"),
            "https://cpa.example.com/usage/",
        )
        self.assertEqual(
            MODULE.usage_center_url("http://127.0.0.1:18317"),
            "http://127.0.0.1:18317/usage/",
        )
        self.assertEqual(MODULE.usage_center_url(""), "")

    @unittest.skipIf(MODULE.ZoneInfo is None, "zoneinfo requires Python 3.9+")
    def test_quota_rows_and_markdown_sort_low_usage_first_with_unavailable_last(self):
        service = MODULE.WeComNotificationService(self.root)
        unavailable = snapshot(5, account="cpa-3")["accounts"][0]
        unavailable["quota"]["status"] = "error"
        payload = {
            "accounts": [
                snapshot(100, account="cpa-10")["accounts"][0],
                unavailable,
                snapshot(10, account="cpa-2")["accounts"][0],
                snapshot(10, account="cpa-1")["accounts"][0],
            ]
        }

        rows = service.quota_rows(payload, threshold_percent=90)
        content = service.build_markdown_v2(
            payload,
            title="CPA 账号额度报告",
            timezone_name="Asia/Shanghai",
            threshold_percent=90,
            now_epoch=1784600000,
        )

        self.assertEqual(
            [(row["account"], row["used_percent"], row["level"]) for row in rows],
            [
                ("cpa-1", 10.0, "normal"),
                ("cpa-2", 10.0, "normal"),
                ("cpa-10", 100.0, "exhausted"),
                ("cpa-3", 5.0, "unavailable"),
            ],
        )
        self.assertLess(content.index("cpa-1"), content.index("cpa-2"))
        self.assertLess(content.index("cpa-2"), content.index("cpa-10"))
        self.assertLess(content.index("cpa-10"), content.index("cpa-3"))
        self.assertEqual(
            [row["account"] for row in service.quota_rows(
                payload,
                threshold_percent=90,
                only_keys={
                    "cpa-10|default:primary_window",
                    "cpa-2|default:primary_window",
                },
            )],
            ["cpa-2", "cpa-10"],
        )

    @unittest.skipIf(MODULE.ZoneInfo is None, "zoneinfo requires Python 3.9+")
    def test_markdown_v2_filters_gpt_53_quota_windows(self):
        service = MODULE.WeComNotificationService(self.root)
        payload = snapshot(90, second_window=True)
        payload["accounts"][0]["quota"]["weekly_windows"][1]["label"] = (
            "GPT-5.3-Codex-Spark"
        )

        rows = service.quota_rows(payload, threshold_percent=90)
        content = service.build_markdown_v2(
            payload,
            title="CPA 账号额度报告",
            timezone_name="Asia/Shanghai",
            threshold_percent=90,
            now_epoch=1784600000,
        )

        self.assertEqual([row["label"] for row in rows], ["常规周限额"])
        self.assertNotIn("GPT-5.3", content)
        self.assertEqual(content.count("alpha"), 1)

        payload["accounts"][0]["quota"]["weekly_windows"] = [
            payload["accounts"][0]["quota"]["weekly_windows"][1]
        ]
        self.assertEqual(service.quota_rows(payload, threshold_percent=90), [])

    @unittest.skipIf(MODULE.ZoneInfo is None, "zoneinfo requires Python 3.9+")
    def test_markdown_v2_rejects_content_over_official_limit(self):
        service = MODULE.WeComNotificationService(self.root)
        large = {"accounts": []}
        for index in range(100):
            item = snapshot()["accounts"][0]
            large["accounts"].append({**item, "id": "account-{:03d}-with-long-name".format(index)})

        with self.assertRaisesRegex(ValueError, "4096"):
            service.build_markdown_v2(
                large,
                title="CPA 账号额度报告",
                timezone_name="Asia/Shanghai",
                threshold_percent=90,
            )

    def test_send_uses_only_markdown_v2_and_checks_wecom_errcode(self):
        requests = []

        def opener(request, timeout):
            requests.append((request, timeout))
            return FakeResponse({"errcode": 0, "errmsg": "ok"})

        service = MODULE.WeComNotificationService(self.root, opener=opener)
        service.set_webhook(WEBHOOK)
        service.send_content("# test")

        request, timeout = requests[0]
        payload = json.loads(request.data.decode("utf-8"))
        self.assertEqual(timeout, 10)
        self.assertEqual(
            payload,
            {"msgtype": "markdown_v2", "markdown_v2": {"content": "# test"}},
        )
        self.assertNotIn("key=", request.data.decode("utf-8"))

        service.opener = lambda *_args, **_kwargs: FakeResponse(
            {"errcode": 93000, "errmsg": "invalid webhook"}
        )
        with self.assertRaisesRegex(RuntimeError, "invalid webhook"):
            service.send_content("# test")

    def test_state_recovers_invalid_nested_maps(self):
        service = MODULE.WeComNotificationService(self.root)
        service.store.write_runtime_state(
            "notification",
            {
                "version": 1,
                "scheduled": [],
                "quota_alerts": "invalid",
                "quota_windows": "invalid",
                "last_error": "previous failure",
            },
        )

        state = service.read_state()

        self.assertEqual(state["scheduled"], {})
        self.assertEqual(state["quota_alerts"], {})
        self.assertEqual(state["quota_windows"], {})
        self.assertEqual(state["last_error"], "previous failure")


@unittest.skipIf(MODULE.ZoneInfo is None, "zoneinfo requires Python 3.9+")
class NotificationSchedulerTests(unittest.TestCase):
    class Control:
        def __init__(self, values):
            self.values = values

        def configuration(self):
            return {"version": 1, "values": dict(self.values)}

    class App:
        def __init__(self, root, values, current_snapshot):
            self.notifications = MODULE.WeComNotificationService(root)
            self.notifications.set_webhook(WEBHOOK)
            self.control = NotificationSchedulerTests.Control(values)
            self.snapshot = current_snapshot
            self.account_calls = 0

        def account_management(self, window):
            assert window == 3600
            self.account_calls += 1
            return self.snapshot

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.values = {
            "notification.enabled": True,
            "notification.timezone": "Asia/Shanghai",
            "notification.daily_times": "09:00,14:00,18:00",
            "notification.schedule_grace_minutes": 15,
            "notification.quota_alert_enabled": True,
            "notification.weekly_threshold_percent": 90.0,
            "usage.quota_cache_seconds": 60,
            "branding.short_name": "Codex CPA",
            "branding.public_base_url": "https://cpa.example.com",
        }

    def tearDown(self):
        self.tmp.cleanup()

    @staticmethod
    def epoch(hour, minute=0, second=0):
        zone = MODULE.ZoneInfo("Asia/Shanghai")
        return int(MODULE.datetime(2026, 7, 20, hour, minute, second, tzinfo=zone).timestamp())

    @staticmethod
    def epoch_in_zone(timezone_name, hour, minute=0, second=0):
        zone = MODULE.ZoneInfo(timezone_name)
        return int(MODULE.datetime(2026, 7, 20, hour, minute, second, tzinfo=zone).timestamp())

    def scheduler(self, current_snapshot=None):
        app = self.App(self.root, self.values, current_snapshot or snapshot())
        sent = []
        app.notifications.send_content = sent.append
        return app, MODULE.NotificationScheduler(app), sent

    def test_scheduled_report_is_idempotent_and_combines_overlapping_slots(self):
        self.values["notification.daily_times"] = "09:00,09:05"
        app, scheduler, sent = self.scheduler()

        first = scheduler.run_once(self.epoch(9, 6))
        second = scheduler.run_once(self.epoch(9, 6, 30))

        self.assertEqual(first["sent"], ["scheduled"])
        self.assertEqual(second["sent"], [])
        self.assertEqual(len(sent), 1)
        self.assertEqual(app.account_calls, 1)
        self.assertEqual(len(app.notifications.read_state()["scheduled"]), 2)

    def test_zero_grace_still_allows_the_scheduled_minute(self):
        self.values["notification.daily_times"] = "09:00"
        self.values["notification.schedule_grace_minutes"] = 0
        _app, scheduler, sent = self.scheduler()

        result = scheduler.run_once(self.epoch(9, 0, 45))

        self.assertEqual(result["sent"], ["scheduled"])
        self.assertEqual(len(sent), 1)

    def test_timezone_change_does_not_reuse_another_zones_schedule_key(self):
        self.values["notification.daily_times"] = "09:00"
        app, scheduler, sent = self.scheduler()

        self.assertEqual(scheduler.run_once(self.epoch(9))["sent"], ["scheduled"])
        self.values["notification.timezone"] = "UTC"
        self.assertEqual(
            scheduler.run_once(self.epoch_in_zone("UTC", 9))["sent"],
            ["scheduled"],
        )

        self.assertEqual(len(sent), 2)
        self.assertEqual(len(app.notifications.read_state()["scheduled"]), 2)

    def test_quota_threshold_state_machine_deduplicates_and_rearms(self):
        self.values["notification.daily_times"] = "23:59"
        app, scheduler, sent = self.scheduler(snapshot(89.99, reset_at=2000))
        alert_key = "alpha|default:primary_window"

        self.assertEqual(scheduler.run_once(self.epoch(10))["sent"], [])
        self.assertEqual(app.notifications.read_state()["quota_alerts"], {})
        app.snapshot = snapshot(90, reset_at=2000)
        self.assertEqual(scheduler.run_once(self.epoch(10, 1))["sent"], ["quota_alert"])
        self.assertIn(alert_key, app.notifications.read_state()["quota_alerts"])
        self.assertIn("🟠 达到预警", sent[-1])
        self.assertEqual(scheduler.run_once(self.epoch(10, 2))["sent"], [])
        app.snapshot = snapshot(100, reset_at=2000)
        self.assertEqual(
            scheduler.run_once(self.epoch(10, 3))["sent"],
            ["quota_exhausted"],
        )
        self.assertIn("🔴 额度耗尽", sent[-1])
        self.assertEqual(
            app.notifications.read_state()["quota_alerts"][alert_key]["level"],
            "exhausted",
        )
        self.assertEqual(scheduler.run_once(self.epoch(10, 4))["sent"], [])
        unavailable = snapshot(100, reset_at=3000)
        unavailable["accounts"][0]["quota"]["status"] = "error"
        app.snapshot = unavailable
        self.assertEqual(scheduler.run_once(self.epoch(10, 5))["sent"], [])
        self.assertIn(alert_key, app.notifications.read_state()["quota_alerts"])
        app.snapshot = snapshot(89, reset_at=3000)
        self.assertEqual(
            scheduler.run_once(self.epoch(10, 6))["sent"],
            ["quota_recovered"],
        )
        self.assertIn("🟢 额度恢复", sent[-1])
        self.assertNotIn(alert_key, app.notifications.read_state()["quota_alerts"])
        app.snapshot = snapshot(90, reset_at=3000)
        self.assertEqual(scheduler.run_once(self.epoch(10, 7))["sent"], ["quota_alert"])

        self.assertEqual(len(sent), 4)
        self.assertIn("CPA 周额度预警", sent[0])

    def test_quota_refresh_notifies_accounts_below_threshold_in_one_webhook(self):
        self.values["notification.daily_times"] = "23:59"
        initial = {
            "accounts": [
                snapshot(25, reset_at=2000, account="account-a")["accounts"][0],
                snapshot(70, reset_at=2000, account="account-b")["accounts"][0],
            ]
        }
        app, scheduler, sent = self.scheduler(initial)

        self.assertEqual(scheduler.run_once(self.epoch(10))["sent"], [])
        self.assertEqual(len(app.notifications.read_state()["quota_windows"]), 2)
        app.snapshot = {
            "accounts": [
                snapshot(2, reset_at=2000, account="account-a")["accounts"][0],
                snapshot(5, reset_at=2000, account="account-b")["accounts"][0],
            ]
        }

        result = scheduler.run_once(self.epoch(10, 1))

        self.assertEqual(result["sent"], ["quota_refreshed"])
        self.assertEqual(len(sent), 1)
        self.assertIn(
            "# {} · 账号额度刷新".format(self.values["branding.short_name"]),
            sent[0],
        )
        self.assertIn("account-a", sent[0])
        self.assertIn("account-b", sent[0])
        self.assertEqual(sent[0].count("| 🔄 额度刷新 |"), 2)
        self.assertEqual(
            {
                item["used_percent"]
                for item in app.notifications.read_state()["quota_windows"].values()
            },
            {2, 5},
        )

    def test_quota_refresh_uses_latest_valid_baseline_and_ignores_reset_time(self):
        self.values["notification.daily_times"] = "23:59"
        app, scheduler, sent = self.scheduler(snapshot(40, reset_at=2000))
        key = "alpha|default:primary_window"

        self.assertEqual(scheduler.run_once(self.epoch(10))["sent"], [])
        self.assertEqual(
            app.notifications.read_state()["quota_windows"][key]["used_percent"],
            40,
        )
        app.snapshot = snapshot(40, reset_at=3000)
        self.assertEqual(scheduler.run_once(self.epoch(10, 1))["sent"], [])
        app.snapshot = snapshot(30, reset_at=4000)
        self.assertEqual(scheduler.run_once(self.epoch(10, 2))["sent"], [])
        self.assertEqual(
            app.notifications.read_state()["quota_windows"][key]["used_percent"],
            30,
        )
        app.snapshot = snapshot(10, reset_at=5000)
        self.assertEqual(scheduler.run_once(self.epoch(10, 3))["sent"], [])
        app.snapshot = snapshot(9, reset_at=6000)
        self.assertEqual(
            scheduler.run_once(self.epoch(10, 4))["sent"],
            ["quota_refreshed"],
        )
        self.assertEqual(len(sent), 1)

    def test_legacy_reset_time_state_only_seeds_new_used_percent_baseline(self):
        self.values["notification.daily_times"] = "23:59"
        app, scheduler, sent = self.scheduler(snapshot(4, reset_at=3000))
        key = "alpha|default:primary_window"
        state = app.notifications.read_state()
        state["quota_windows"] = {
            key: {"reset_at": 2000, "observed_at": self.epoch(9)},
        }
        app.notifications.write_state(state)

        self.assertEqual(scheduler.run_once(self.epoch(10))["sent"], [])
        self.assertEqual(sent, [])
        self.assertEqual(
            app.notifications.read_state()["quota_windows"][key]["used_percent"],
            4,
        )

    def test_failed_quota_refresh_notification_still_advances_baseline(self):
        self.values["notification.daily_times"] = "23:59"
        app, scheduler, sent = self.scheduler(snapshot(40, reset_at=2000))
        key = "alpha|default:primary_window"
        self.assertEqual(scheduler.run_once(self.epoch(10))["sent"], [])
        app.snapshot = snapshot(3, reset_at=3000)

        def fail_send(_content):
            raise RuntimeError("temporary webhook failure")

        app.notifications.send_content = fail_send
        with self.assertRaisesRegex(RuntimeError, "temporary webhook failure"):
            scheduler.run_once(self.epoch(10, 1))

        self.assertEqual(
            app.notifications.read_state()["quota_windows"][key]["used_percent"],
            3,
        )
        app.notifications.send_content = sent.append
        self.assertEqual(
            scheduler.run_once(self.epoch(10, 2))["sent"],
            [],
        )
        self.assertEqual(
            app.notifications.read_state()["quota_windows"][key]["used_percent"],
            3,
        )

    def test_scheduled_report_includes_quota_refresh_without_duplicate_webhook(self):
        self.values["notification.daily_times"] = "09:01"
        app, scheduler, sent = self.scheduler(snapshot(40, reset_at=2000))
        self.assertEqual(scheduler.run_once(self.epoch(9))["sent"], [])
        app.snapshot = snapshot(4, reset_at=2000)

        result = scheduler.run_once(self.epoch(9, 1))

        self.assertEqual(result["sent"], ["scheduled"])
        self.assertEqual(len(sent), 1)
        self.assertIn("# Codex CPA · 账号额度报告", sent[0])
        self.assertIn("🔄 额度刷新", sent[0])
        self.assertIn("alpha", sent[0])

    def test_recovery_groups_all_accounts_into_one_webhook(self):
        self.values["notification.daily_times"] = "23:59"
        initial = {
            "accounts": [
                snapshot(95, account="account-a")["accounts"][0],
                snapshot(100, account="account-b")["accounts"][0],
            ]
        }
        app, scheduler, sent = self.scheduler(initial)

        self.assertEqual(
            scheduler.run_once(self.epoch(10))["sent"],
            ["quota_transition"],
        )
        app.snapshot = {
            "accounts": [
                snapshot(10, account="account-a")["accounts"][0],
                snapshot(20, account="account-b")["accounts"][0],
            ]
        }

        result = scheduler.run_once(self.epoch(10, 1))

        self.assertEqual(result["sent"], ["quota_recovered"])
        self.assertEqual(len(sent), 2)
        self.assertIn("# CPA 额度恢复", sent[-1])
        self.assertIn("account-a", sent[-1])
        self.assertIn("account-b", sent[-1])
        self.assertEqual(sent[-1].count("🟢 额度恢复"), 2)
        self.assertEqual(app.notifications.read_state()["quota_alerts"], {})

    def test_failed_recovery_send_keeps_state_and_retries(self):
        self.values["notification.daily_times"] = "23:59"
        app, scheduler, sent = self.scheduler(snapshot(100))
        alert_key = "alpha|default:primary_window"
        self.assertEqual(
            scheduler.run_once(self.epoch(10))["sent"],
            ["quota_exhausted"],
        )
        app.snapshot = snapshot(10)

        def fail_send(_content):
            raise RuntimeError("temporary webhook failure")

        app.notifications.send_content = fail_send

        with self.assertRaisesRegex(RuntimeError, "temporary webhook failure"):
            scheduler.run_once(self.epoch(10, 1))

        self.assertIn(alert_key, app.notifications.read_state()["quota_alerts"])
        app.notifications.send_content = sent.append
        self.assertEqual(
            scheduler.run_once(self.epoch(10, 2))["sent"],
            ["quota_recovered"],
        )
        self.assertNotIn(alert_key, app.notifications.read_state()["quota_alerts"])

    def test_scheduled_report_absorbs_grouped_recovery_without_second_webhook(self):
        self.values["notification.daily_times"] = "09:01"
        initial = {
            "accounts": [
                snapshot(95, account="account-a")["accounts"][0],
                snapshot(100, account="account-b")["accounts"][0],
            ]
        }
        app, scheduler, sent = self.scheduler(initial)
        self.assertEqual(
            scheduler.run_once(self.epoch(9))["sent"],
            ["quota_transition"],
        )
        app.snapshot = {
            "accounts": [
                snapshot(10, account="account-a")["accounts"][0],
                snapshot(20, account="account-b")["accounts"][0],
            ]
        }

        result = scheduler.run_once(self.epoch(9, 1))

        self.assertEqual(result["sent"], ["scheduled"])
        self.assertEqual(len(sent), 2)
        self.assertIn("# Codex CPA · 账号额度报告", sent[-1])
        self.assertIn("account-a", sent[-1])
        self.assertIn("account-b", sent[-1])
        self.assertEqual(sent[-1].count("🟢 额度恢复"), 2)
        self.assertEqual(app.notifications.read_state()["quota_alerts"], {})

    def test_quota_collection_uses_cache_interval_and_scheduled_report_consumes_alert(self):
        self.values["notification.daily_times"] = "09:00"
        app, scheduler, sent = self.scheduler(snapshot(90, reset_at=2000))

        result = scheduler.run_once(self.epoch(9, 0, 10))
        within_cache = scheduler.run_once(self.epoch(9, 0, 40))
        after_cache = scheduler.run_once(self.epoch(9, 1, 10))

        self.assertEqual(result["sent"], ["scheduled"])
        self.assertEqual(within_cache["sent"], [])
        self.assertEqual(after_cache["sent"], [])
        self.assertEqual(app.account_calls, 2)
        self.assertEqual(len(sent), 1)
        self.assertIn("🟠 达到预警", sent[0])

    def test_legacy_resolved_record_does_not_suppress_first_breach(self):
        self.values["notification.daily_times"] = "23:59"
        app, scheduler, sent = self.scheduler(snapshot(90, reset_at=2000))
        state = app.notifications.read_state()
        state["quota_alerts"] = {
            "alpha|default:primary_window": {
                "reset_at": 2000,
                "level": "normal",
                "threshold": 90.0,
                "alerted_at": None,
            }
        }
        app.notifications.write_state(state)

        self.assertEqual(scheduler.run_once(self.epoch(10))["sent"], ["quota_alert"])
        self.assertEqual(len(sent), 1)

    def test_enabling_alerts_notifies_existing_breach(self):
        self.values["notification.daily_times"] = "23:59"
        self.values["notification.quota_alert_enabled"] = False
        app, scheduler, sent = self.scheduler(snapshot(95, reset_at=2000))

        self.assertEqual(scheduler.run_once(self.epoch(10))["sent"], [])
        self.assertEqual(app.account_calls, 0)
        self.values["notification.quota_alert_enabled"] = True
        self.assertEqual(scheduler.run_once(self.epoch(10, 1))["sent"], ["quota_alert"])
        self.assertEqual(len(sent), 1)

    def test_disabled_scheduler_preserves_last_error(self):
        self.values["notification.enabled"] = False
        app, scheduler, _sent = self.scheduler()
        state = app.notifications.read_state()
        state["last_error"] = "previous failure"
        app.notifications.write_state(state)

        result = scheduler.run_once(self.epoch(10))

        self.assertFalse(result["enabled"])
        self.assertEqual(app.notifications.read_state()["last_error"], "previous failure")
