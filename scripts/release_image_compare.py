#!/usr/bin/env python3
"""Compare runtime-relevant content of published component images."""

import argparse
import json
import subprocess


EDGE_RUNTIME_CONFIG_FIELDS = (
    "Cmd",
    "Entrypoint",
    "Env",
    "ExposedPorts",
    "Healthcheck",
    "OnBuild",
    "Shell",
    "StopSignal",
    "User",
    "Volumes",
    "WorkingDir",
)
EDGE_RUNTIME_FINGERPRINT_COMMAND = """
set -eu
sha256sum \
  /usr/local/openresty/nginx/conf/nginx.conf \
  /usr/local/openresty/bin/openresty
stat -c '%a:%u:%g:%s:%n' \
  /var/run/cliproxy-edge \
  /var/log/cliproxy \
  /var/run/cliproxy-edge/active-gateway.conf
cat /var/run/cliproxy-edge/active-gateway.conf
""".strip()


def inspect_image(image):
    payload = subprocess.check_output(
        ["docker", "image", "inspect", image],
        text=True,
    )
    records = json.loads(payload)
    if not isinstance(records, list) or len(records) != 1:
        raise RuntimeError("Docker image inspection returned an unexpected result")
    return records[0]


def edge_runtime_fingerprint(image):
    return subprocess.check_output(
        [
            "docker",
            "run",
            "--rm",
            "--entrypoint",
            "sh",
            image,
            "-c",
            EDGE_RUNTIME_FINGERPRINT_COMMAND,
        ],
        text=True,
    ).strip()


def edge_runtime_equivalent(current, candidate, current_fingerprint, candidate_fingerprint):
    if current.get("Architecture") != candidate.get("Architecture"):
        return False
    if current.get("Os") != candidate.get("Os"):
        return False
    current_config = current.get("Config") or {}
    candidate_config = candidate.get("Config") or {}
    if any(
        current_config.get(field) != candidate_config.get(field)
        for field in EDGE_RUNTIME_CONFIG_FIELDS
    ):
        return False
    current_layers = (current.get("RootFS") or {}).get("Layers") or []
    candidate_layers = (candidate.get("RootFS") or {}).get("Layers") or []
    if len(current_layers) < 2 or len(current_layers) != len(candidate_layers):
        return False
    # edge/Dockerfile adds exactly two filesystem layers after the pinned base:
    # COPY nginx.conf, then create the runtime directories/default slot file.
    if current_layers[:-2] != candidate_layers[:-2]:
        return False
    return current_fingerprint == candidate_fingerprint


def compare_edge_images(current_image, candidate_image):
    current = inspect_image(current_image)
    candidate = inspect_image(candidate_image)
    return edge_runtime_equivalent(
        current,
        candidate,
        edge_runtime_fingerprint(current_image),
        edge_runtime_fingerprint(candidate_image),
    )


def build_parser():
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)
    edge = subparsers.add_parser("edge-runtime-equivalent")
    edge.add_argument("current_image")
    edge.add_argument("candidate_image")
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    if args.command == "edge-runtime-equivalent":
        return 0 if compare_edge_images(args.current_image, args.candidate_image) else 1
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
