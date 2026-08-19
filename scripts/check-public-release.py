#!/usr/bin/env python3
"""Reject repository content that should never enter a public source/image release."""

import argparse
import ipaddress
import re
import subprocess
from pathlib import Path


TEXT_MAX_BYTES = 4 * 1024 * 1024
ALLOWED_DOMAIN_SUFFIXES = (
    "alpinelinux.org",
    "aliyun.com",
    "chatgpt.com",
    "daocloud.io",
    "debian.org",
    "docker.io",
    "example.com",
    "example.net",
    "example.org",
    "github.com",
    "ghcr.io",
    "invalid",
    "openai.com",
    "python.org",
    "shields.io",
    "tsinghua.edu.cn",
    "test",
    "w3.org",
    "weixin.qq.com",
)
FORBIDDEN_TRACKED_PARTS = {
    "auth",
    "backups",
    "configs",
    "logs",
    "secrets",
    "state",
}
LOCAL_ONLY_TRACKED_PARTS = {".harness"}
FORBIDDEN_TRACKED_PATHS = {"AGENTS.md"}
URL_HOST_PATTERN = re.compile(
    r"https?://(?:[^/@\s]+@)?((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,63})",
    re.IGNORECASE,
)
REGISTRY_HOST_PATTERN = re.compile(
    r"(?<![A-Za-z0-9_-])((?:[A-Za-z0-9-]+\.)+[A-Za-z]{2,63})/"
    r"[A-Za-z0-9._/-]+(?=[:@])"
)
EMAIL_PATTERN = re.compile(r"[A-Za-z0-9._%+-]+@([A-Za-z0-9.-]+\.[A-Za-z]{2,63})")
IPV4_PATTERN = re.compile(r"(?<![0-9.])(?:\d{1,3}\.){3}\d{1,3}(?![0-9.])")
WEBHOOK_SECRET_PATTERN = re.compile(
    r"https?://[^\s'\"]+/cgi-bin/webhook/send\?key="
    r"(?!(?:test|example|placeholder)[-_])[A-Za-z0-9_-]+",
    re.IGNORECASE,
)
SECRET_MARKERS = (
    "-----BEGIN PRIVATE KEY-----",
    "-----BEGIN OPENSSH PRIVATE KEY-----",
    "-----BEGIN RSA PRIVATE KEY-----",
)


def _literal(*parts):
    return re.compile(re.escape("".join(parts)), re.IGNORECASE)


# Keep organization, internal project and former contributor identifiers out of
# public paths and file contents. Build the literals from fragments so this
# guard does not reintroduce the identifiers it is designed to reject.
ORGANIZATION_MARKER_PATTERNS = (
    _literal("wo", "qu"),
    _literal("q", "data"),
    _literal("q", "arch"),
    _literal("q", "fusion"),
    _literal("chen", "hui", ".", "shang"),
    re.compile(r"(?<![A-Za-z0-9])" + "w" + "q" + r"(?![A-Za-z0-9])", re.IGNORECASE),
    re.compile("Q" + "AI-" + r"[0-9]+", re.IGNORECASE),
)


def organization_marker(text):
    return any(pattern.search(text) for pattern in ORGANIZATION_MARKER_PATTERNS)


def tracked_files(root):
    result = subprocess.run(
        [
            "git",
            "-C",
            str(root),
            "ls-files",
            "--cached",
            "--others",
            "--exclude-standard",
            "-z",
        ],
        check=True,
        stdout=subprocess.PIPE,
    )
    return [Path(item.decode("utf-8")) for item in result.stdout.split(b"\0") if item]


def allowed_domain(host):
    normalized = host.lower().rstrip(".")
    return any(
        normalized == suffix or normalized.endswith("." + suffix)
        for suffix in ALLOWED_DOMAIN_SUFFIXES
    )


def scan(root):
    problems = []
    for relative in tracked_files(root):
        if organization_marker(relative.as_posix()):
            problems.append((relative, "路径包含组织、内部项目或人员标识"))
        if relative.as_posix() in FORBIDDEN_TRACKED_PATHS:
            problems.append((relative, "本地 Agent/部署上下文被 Git 跟踪"))
            continue
        if LOCAL_ONLY_TRACKED_PARTS.intersection(relative.parts):
            problems.append((relative, "本地 Agent/部署上下文被 Git 跟踪"))
            continue
        if FORBIDDEN_TRACKED_PARTS.intersection(relative.parts):
            problems.append((relative, "运行态/敏感目录中的文件被 Git 跟踪"))
            continue
        path = root / relative
        if not path.is_file() or path.stat().st_size > TEXT_MAX_BYTES:
            continue
        raw = path.read_bytes()
        if b"\0" in raw:
            continue
        text = raw.decode("utf-8", errors="replace")
        if organization_marker(text):
            problems.append((relative, "内容包含组织、内部项目或人员标识"))
        if relative.as_posix() == "scripts/check-public-release.py":
            continue
        for marker in SECRET_MARKERS:
            if marker in text:
                problems.append((relative, "包含私钥材料"))
        if WEBHOOK_SECRET_PATTERN.search(text):
            problems.append((relative, "包含带密钥的 Webhook URL"))
        for match in IPV4_PATTERN.finditer(text):
            try:
                address = ipaddress.ip_address(match.group(0))
            except ValueError:
                continue
            if (
                address in ipaddress.ip_network("10.0.0.0/8")
                or address in ipaddress.ip_network("172.16.0.0/12")
                or address in ipaddress.ip_network("192.168.0.0/16")
            ):
                problems.append((relative, "包含私网 IP 地址"))
                break
        for match in EMAIL_PATTERN.finditer(text):
            if not allowed_domain(match.group(1)):
                problems.append((relative, "包含非示例邮箱域名"))
                break
        fixed_hosts = URL_HOST_PATTERN.findall(text) + REGISTRY_HOST_PATTERN.findall(text)
        for host in fixed_hosts:
            if not allowed_domain(host):
                problems.append((relative, "包含未批准的固定域名"))
                break
    return sorted(set(problems), key=lambda item: (item[0].as_posix(), item[1]))


def main(argv=None):
    parser = argparse.ArgumentParser(description="检查公开发布边界")
    parser.add_argument("--root", default=".")
    args = parser.parse_args(argv)
    root = Path(args.root).resolve()
    problems = scan(root)
    if problems:
        for path, message in problems:
            print("{}: {}".format(path.as_posix(), message))
        raise SystemExit("公开发布检查失败：发现 {} 项问题".format(len(problems)))
    print(
        "公开发布检查通过：未发现运行态文件、组织/人员标识、私网地址、"
        "组织域名或明文密钥"
    )


if __name__ == "__main__":
    main()
