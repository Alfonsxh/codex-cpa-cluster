#!/usr/bin/env python3
"""Find a working isolated account and compare v1/Go-v2 Responses safely.

The management key and dedicated Test API Key are read as two stdin lines.
Every route mutation uses the supported Portal API on two distinct isolated
state copies.  Original routes and password-reset state are restored in a
finally block.  Reports contain only one-way identity/account digests and
sanitized data-plane summaries; they never contain credentials, user/account
identifiers, response text, model names, cookies, CSRF tokens, or SSE data.
"""

import argparse
import importlib.util
import json
import sqlite3
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


PORTAL = load_module(
    "migration_portal_write_compare",
    ROOT / "scripts" / "migration-portal-write-compare.py",
)
DATA = load_module(
    "migration_data_plane_compare",
    ROOT / "scripts" / "migration-data-plane-compare.py",
)


def read_route(database_path, user):
    database = sqlite3.connect(
        "file:{}?mode=ro".format(database_path.resolve()), uri=True
    )
    try:
        row = database.execute(
            "SELECT account_id FROM user_routes WHERE lower(trim(user_email)) = ?",
            (PORTAL.normalize_user(user),),
        ).fetchone()
    finally:
        database.close()
    return str(row[0] or "").strip() if row else ""


def set_route(target_run, route, step):
    response = target_run.portal_request(
        "PUT", "/usage/me/group", {"group_id": route}
    )
    target_run.require_status(step, response, 200)
    readback = target_run.portal_request("GET", "/usage/me/route")
    payload = target_run.require_status(step + "_readback", readback, 200)
    if payload.get("current_group") != route:
        raise PORTAL.ComparisonFailure(step + "_readback", "route_not_changed")
    target_run.route_changed = route != target_run.original_route


def sanitized_target_run(target_run):
    status_counts = {}
    for status in target_run.operational.values():
        key = "{}|{}".format(
            status.get("code", "unknown"),
            str(status.get("selectable") is True).lower(),
        )
        status_counts[key] = status_counts.get(key, 0) + 1
    return {
        "surface": target_run.target.surface,
        "base_url": target_run.target.base_url,
        "probe_user_sha256": PORTAL.digest(target_run.user),
        "original_route_sha256": (
            PORTAL.digest(target_run.original_route)
            if target_run.original_route
            else ""
        ),
        "account_count": len(target_run.catalog),
        "selectable_count": len(target_run.selectable),
        "account_status_counts": status_counts,
        "prepared": target_run.prepared,
        "cleaned": target_run.cleaned,
        "failures": list(target_run.failures),
    }


def run(targets, management_key, test_key, timeout):
    PORTAL.validate_pair(targets)
    data_targets = [
        DATA.Target(
            name=target.name,
            surface=target.surface,
            base_url=target.base_url,
            control_db=target.control_db,
        )
        for target in targets
    ]
    target_runs = [
        PORTAL.TargetRun(target, management_key, test_key, timeout)
        for target in targets
    ]
    attempts = []
    selected_route_digest = ""
    workflow_failures = []
    original_routes = {}
    route_restored = {target.name: False for target in targets}

    try:
        for target_run in target_runs:
            try:
                target_run.prepare()
                original_routes[target_run.target.name] = target_run.original_route
            except PORTAL.ComparisonFailure as error:
                target_run.failures.append(
                    {"step": error.step, "reason": error.reason}
                )
            except Exception:
                target_run.failures.append(
                    {"step": "prepare", "reason": "unexpected_exception"}
                )

        if not all(target_run.prepared for target_run in target_runs):
            workflow_failures.append({"step": "prepare", "reason": "target_failed"})
        elif len({target_run.user for target_run in target_runs}) != 1:
            workflow_failures.append(
                {"step": "identity", "reason": "test_user_mismatch"}
            )
        else:
            common_selectable = set.intersection(
                *(target_run.selectable for target_run in target_runs)
            )
            original_route_set = {
                target_run.original_route for target_run in target_runs
            }
            candidates = sorted(
                common_selectable - original_route_set, key=PORTAL.digest
            )
            if not candidates:
                workflow_failures.append(
                    {"step": "route_selection", "reason": "no_safe_alternate"}
                )

            for index, candidate in enumerate(candidates, start=1):
                route_digest = PORTAL.digest(candidate)
                attempt = {
                    "candidate_index": index,
                    "route_sha256": route_digest,
                    "route_mutations": {},
                    "data_plane": None,
                }
                try:
                    for target_run in target_runs:
                        step = "candidate_{}_route".format(index)
                        set_route(target_run, candidate, step)
                        attempt["route_mutations"][target_run.target.name] = {
                            "surface": target_run.target.surface,
                            "passed": True,
                        }
                    attempt["data_plane"] = DATA.run(
                        data_targets, test_key, timeout
                    )
                    if attempt["data_plane"].get("compatible"):
                        selected_route_digest = route_digest
                        attempts.append(attempt)
                        break
                except PORTAL.ComparisonFailure as error:
                    attempt["failure"] = {
                        "step": error.step,
                        "reason": error.reason,
                    }
                except Exception as error:
                    attempt["failure"] = {
                        "step": "candidate_probe",
                        "reason": type(error).__name__,
                    }
                finally:
                    for target_run in target_runs:
                        original = original_routes.get(target_run.target.name)
                        if not original or not target_run.portal_cookie:
                            continue
                        try:
                            set_route(
                                target_run,
                                original,
                                "candidate_{}_restore".format(index),
                            )
                            target_run.route_changed = False
                        except PORTAL.ComparisonFailure as error:
                            target_run.failures.append(
                                {"step": error.step, "reason": error.reason}
                            )
                        except Exception:
                            target_run.failures.append(
                                {
                                    "step": "candidate_restore",
                                    "reason": "unexpected_exception",
                                }
                            )
                if attempt not in attempts:
                    attempts.append(attempt)
    finally:
        for target_run in target_runs:
            if not target_run.cleaned:
                target_run.cleanup()
        for target_run in target_runs:
            original = original_routes.get(target_run.target.name, "")
            route_restored[target_run.target.name] = bool(
                original
                and read_route(target_run.target.control_db, target_run.user)
                == original
            )

    target_reports = {
        target_run.target.name: {
            **sanitized_target_run(target_run),
            "route_restored": route_restored[target_run.target.name],
        }
        for target_run in target_runs
    }
    for name, target_report in target_reports.items():
        if (
            target_report["failures"]
            or not target_report["cleaned"]
            or not target_report["route_restored"]
        ):
            workflow_failures.append(
                {"step": "cleanup", "reason": "target_failed", "target": name}
            )

    compatible = bool(selected_route_digest) and not workflow_failures
    return {
        "version": 1,
        "compatible": compatible,
        "candidate_count": len(attempts),
        "selected_route_sha256": selected_route_digest,
        "targets": target_reports,
        "attempts": attempts,
        "workflow_failures": workflow_failures,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", action="append", type=PORTAL.parse_target, required=True)
    parser.add_argument("--timeout", type=float, default=120.0)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--credentials-stdin", action="store_true", required=True)
    parser.add_argument(
        "--confirm-isolated-route-data-test", action="store_true", required=True
    )
    parser.add_argument("--summary", action="store_true")
    args = parser.parse_args(argv)
    management_key = sys.stdin.readline().strip()
    test_key = sys.stdin.readline().strip()
    if not management_key or not test_key:
        raise SystemExit("management key and dedicated Test Key require two stdin lines")
    if len(management_key) > 4096 or len(test_key) > 4096:
        raise SystemExit("stdin credential length is invalid")
    report = run(args.target, management_key, test_key, args.timeout)
    rendered = json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if args.output:
        args.output.write_text(rendered, encoding="utf-8")
    if args.summary:
        summary = {
            "version": report["version"],
            "compatible": report["compatible"],
            "candidate_count": report["candidate_count"],
            "selected_route": bool(report["selected_route_sha256"]),
            "targets": {
                name: {
                    key: value
                    for key, value in target.items()
                    if key
                    in {
                        "surface",
                        "prepared",
                        "cleaned",
                        "route_restored",
                        "selectable_count",
                        "account_status_counts",
                    }
                }
                for name, target in report["targets"].items()
            },
            "attempts": [
                {
                    "candidate_index": attempt["candidate_index"],
                    "compatible": bool(
                        attempt.get("data_plane", {}).get("compatible")
                    ),
                    "failure": attempt.get("failure"),
                }
                for attempt in report["attempts"]
            ],
            "workflow_failures": report["workflow_failures"],
        }
        print(json.dumps(summary, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        print(rendered, end="")
    management_key = ""
    test_key = ""
    return 0 if report["compatible"] else 1


if __name__ == "__main__":
    sys.exit(main())
