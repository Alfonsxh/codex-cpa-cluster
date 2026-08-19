#!/usr/bin/env python3
"""Read and atomically switch the stable Edge proxy's active Gateway slot."""

import argparse
import os
import re
from pathlib import Path


SLOTS = ("blue", "green")
ACTIVE_CONFIG = Path("state/edge/active-gateway.conf")
ACTIVE_RE = re.compile(
    r"^set\s+\$active_gateway_backend\s+gateway-(blue|green):8317;$"
)


def normalize_slot(value):
    slot = str(value or "").strip().lower()
    if slot not in SLOTS:
        raise ValueError("Gateway slot must be blue or green")
    return slot


def inactive_slot(slot):
    return "green" if normalize_slot(slot) == "blue" else "blue"


def config_path(root):
    return Path(root).resolve() / ACTIVE_CONFIG


def render(slot):
    return "set $active_gateway_backend gateway-{}:8317;\n".format(
        normalize_slot(slot)
    )


def read_active_slot(root, fallback=""):
    path = config_path(root)
    if path.is_symlink():
        raise ValueError("active Gateway slot file must not be a symlink")
    if path.exists() and not path.is_file():
        raise ValueError("active Gateway slot path must be a regular file")
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except FileNotFoundError:
        if fallback:
            return normalize_slot(fallback)
        raise ValueError("active Gateway slot file is missing: {}".format(path))
    directives = [
        line.strip()
        for line in lines
        if line.strip() and not line.lstrip().startswith("#")
    ]
    if len(directives) != 1:
        raise ValueError("active Gateway slot file must contain exactly one directive")
    match = ACTIVE_RE.fullmatch(directives[0])
    if not match:
        raise ValueError("active Gateway slot file contains an unsafe directive")
    return match.group(1)


def write_active_slot(root, slot):
    path = config_path(root)
    path.parent.mkdir(parents=True, exist_ok=True)
    os.chmod(path.parent, 0o750)
    temporary = path.with_name(".{}.{}.tmp".format(path.name, os.getpid()))
    temporary.write_text(render(slot), encoding="utf-8")
    os.chmod(temporary, 0o644)
    os.replace(temporary, path)
    directory_fd = os.open(path.parent, os.O_RDONLY)
    try:
        os.fsync(directory_fd)
    finally:
        os.close(directory_fd)
    return path


def ensure_active_slot(root, fallback="blue"):
    """Keep a valid existing selection or atomically create the fixed include."""
    path = config_path(root)
    if not path.exists() and not path.is_symlink():
        write_active_slot(root, normalize_slot(fallback))
    return read_active_slot(root)


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", required=True)
    subparsers = parser.add_subparsers(dest="command", required=True)
    read_parser = subparsers.add_parser("read")
    read_parser.add_argument("--fallback", default="")
    write_parser = subparsers.add_parser("write")
    write_parser.add_argument("slot", choices=SLOTS)
    ensure_parser = subparsers.add_parser("ensure")
    ensure_parser.add_argument("--fallback", default="blue")
    subparsers.add_parser("inactive")
    args = parser.parse_args(argv)

    if args.command == "read":
        print(read_active_slot(args.root, fallback=args.fallback))
    elif args.command == "write":
        write_active_slot(args.root, args.slot)
        print(args.slot)
    elif args.command == "ensure":
        print(ensure_active_slot(args.root, fallback=args.fallback))
    else:
        print(inactive_slot(read_active_slot(args.root)))


if __name__ == "__main__":
    main()
