#!/usr/bin/env python3
"""Bound host-mounted control-plane logs with copy-truncate rotation."""

import argparse
import json
import os
import shutil
import signal
import sys
import threading
import time
from pathlib import Path


APPLICATION_ROOT = Path(
    os.environ.get("CLIPROXY_APP_ROOT", Path(__file__).resolve().parents[1])
).resolve()
PROJECT_ROOT = Path(os.environ.get("CLIPROXY_ROOT", APPLICATION_ROOT)).resolve()
sys.path.insert(0, str(APPLICATION_ROOT / "scripts"))
from control_plane_store import ControlPlaneStore  # noqa: E402
DEFAULT_TARGETS = (
    "logs/gateway/access.tsv",
    "logs/gateway/admin-access.log",
    "logs/gateway/error.log",
    "logs/gateway/edge-error.log",
    "logs/admin/audit.jsonl",
)

def _write_state(root, payload):
    ControlPlaneStore(root).write_runtime_state("log_maintenance", payload)


def _read_state(root):
    payload = ControlPlaneStore(root).read_runtime_state("log_maintenance", {})
    return payload if isinstance(payload, dict) else {}


def rotate_file(path, max_bytes, backups):
    path = Path(path)
    if not path.exists():
        return False
    if path.is_symlink() or not path.is_file():
        raise ValueError("refusing to rotate non-regular log: {}".format(path))
    if path.stat().st_size <= max_bytes:
        return False

    oldest = path.with_name("{}.{}".format(path.name, backups))
    if oldest.exists():
        oldest.unlink()
    for index in range(backups - 1, 0, -1):
        source = path.with_name("{}.{}".format(path.name, index))
        if source.exists():
            os.replace(
                source,
                path.with_name("{}.{}".format(path.name, index + 1)),
            )

    backup = path.with_name("{}.1".format(path.name))
    shutil.copy2(path, backup)
    with path.open("r+b") as handle:
        handle.truncate(0)
    return True


def run_once(root=PROJECT_ROOT, max_file_size_mb=32, backups=2, now=None):
    root = Path(root).resolve()
    logs_root = (root / "logs").resolve()
    max_bytes = int(max_file_size_mb) * 1024 * 1024
    backups = int(backups)
    if max_bytes <= 0 or backups <= 0:
        raise ValueError("log size and backup count must be positive")

    rotated = []
    errors = []
    for relative in DEFAULT_TARGETS:
        path = (root / relative).resolve()
        if logs_root not in path.parents:
            errors.append("invalid log target: {}".format(relative))
            continue
        try:
            if rotate_file(path, max_bytes=max_bytes, backups=backups):
                rotated.append(relative)
        except (OSError, ValueError) as error:
            errors.append("{}: {}".format(relative, error))

    heartbeat = int(time.time()) if now is None else int(now)
    previous = _read_state(root)
    payload = {
        "heartbeat_at": heartbeat,
        "last_error": "; ".join(errors)[:500],
        "rotations": int(previous.get("rotations") or 0) + len(rotated),
        "last_rotated": rotated,
        "max_file_size_mb": int(max_file_size_mb),
        "backups": backups,
    }
    _write_state(root, payload)
    return payload


def healthy(root=PROJECT_ROOT, now=None, max_age_seconds=300):
    now = int(time.time()) if now is None else int(now)
    state = _read_state(root)
    heartbeat = int(state.get("heartbeat_at") or 0)
    return bool(
        heartbeat
        and now - heartbeat <= int(max_age_seconds)
        and not state.get("last_error")
    )


def build_parser():
    parser = argparse.ArgumentParser(description="CLIProxyAPI host log maintenance")
    parser.add_argument("--root", default=str(PROJECT_ROOT))
    parser.add_argument("--interval", type=float, default=60)
    parser.add_argument("--max-file-size-mb", type=int, default=32)
    parser.add_argument("--backups", type=int, default=2)
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--health", action="store_true")
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    if args.health:
        return 0 if healthy(args.root) else 1
    if args.once:
        payload = run_once(
            args.root,
            max_file_size_mb=args.max_file_size_mb,
            backups=args.backups,
        )
        print(json.dumps(payload, ensure_ascii=False, separators=(",", ":")))
        return 0 if not payload["last_error"] else 1

    stopping = threading.Event()

    def stop(*unused):
        stopping.set()

    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    while not stopping.is_set():
        try:
            payload = run_once(
                args.root,
                max_file_size_mb=args.max_file_size_mb,
                backups=args.backups,
            )
            if payload["last_rotated"] or payload["last_error"]:
                print(
                    json.dumps(payload, ensure_ascii=False, separators=(",", ":")),
                    flush=True,
                )
        except Exception as error:
            print(
                "log maintenance failed: {}: {}".format(type(error).__name__, error),
                file=sys.stderr,
                flush=True,
            )
        stopping.wait(max(5, float(args.interval)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
