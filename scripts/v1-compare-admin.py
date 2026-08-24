#!/usr/bin/env python3
"""Run immutable v1 Admin APIs without autonomous scheduler threads.

This entrypoint exists only for the isolated v1/main comparison topology. It
imports the Admin implementation and bounded HTTP server from the selected
immutable image, but does not start the quota-failover or enterprise-
notification schedulers. Paired v1/v2 reads are therefore not racing an
autonomous mutation of the comparison baseline.
"""

import http.client
import json
import os
import re
import socket
import stat
import subprocess
import sys
from pathlib import Path


class _UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, socket_path):
        super().__init__("docker", timeout=3)
        self.socket_path = str(socket_path)

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.socket_path)


def _docker_read_request(socket_path, method, path):
    connection = _UnixHTTPConnection(socket_path)
    try:
        connection.request(method, path, headers={"Host": "docker"})
        response = connection.getresponse()
        payload = response.read(2 * 1024 * 1024 + 1)
        if len(payload) > 2 * 1024 * 1024:
            raise RuntimeError("v1 comparison Docker read response is too large")
        return response.status, payload
    finally:
        connection.close()


def _normalize_container_rows(containers, expected_project):
    if not isinstance(containers, list):
        raise RuntimeError("v1 comparison Docker container catalog is invalid")
    output = []
    for item in containers:
        if not isinstance(item, dict):
            raise RuntimeError("v1 comparison Docker container row is invalid")
        labels = item.get("Labels")
        if not isinstance(labels, dict) or labels.get(
            "com.docker.compose.project"
        ) != expected_project:
            raise RuntimeError("v1 comparison Docker read scope is wider than expected")
        service = str(labels.get("com.docker.compose.service") or "").strip()
        if not service:
            continue
        names = item.get("Names")
        name = str(names[0]).lstrip("/") if isinstance(names, list) and names else ""
        status = str(item.get("Status") or "")
        health_match = re.search(r"\((healthy|unhealthy|health: starting)\)", status)
        health = health_match.group(1) if health_match else ""
        if health.startswith("health: "):
            health = health.split(": ", 1)[1]
        output.append(
            {
                "service": service,
                "name": name,
                "state": str(item.get("State") or "unknown").lower(),
                "status": status,
                "health": health,
            }
        )
    return output


def _load_compare_compose_ps(socket_path, expected_project):
    status, payload = _docker_read_request(socket_path, "GET", "/containers/json?all=1")
    if status != 200:
        raise RuntimeError("v1 comparison Docker read endpoint is unavailable")
    try:
        containers = json.loads(payload)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError("v1 comparison Docker read response is invalid") from error
    return _normalize_container_rows(containers, expected_project)


def _read_compare_auth_snapshot(control):
    """Return one validated auth snapshot from an explicitly isolated root."""

    root = Path(control.root).resolve()
    if root == Path("/") or not (root / ".v2-isolated-copy.json").is_file():
        raise RuntimeError("v1 comparison auth snapshot requires an isolated-copy marker")

    snapshot_path = Path(control.auth_snapshot_path)
    try:
        raw = snapshot_path.read_bytes()
    except OSError as error:
        raise RuntimeError("v1 comparison auth snapshot is unavailable") from error
    if not raw or len(raw) > 16 * 1024 * 1024:
        raise RuntimeError("v1 comparison auth snapshot has an invalid size")
    try:
        payload = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError("v1 comparison auth snapshot is invalid") from error
    generation = payload.get("generation") if isinstance(payload, dict) else None
    records = payload.get("records") if isinstance(payload, dict) else None
    if not isinstance(generation, str) or not re.fullmatch(r"[0-9a-f]{32}", generation):
        raise RuntimeError("v1 comparison auth snapshot generation is invalid")
    if not isinstance(records, list):
        raise RuntimeError("v1 comparison auth snapshot records are invalid")
    return payload


def _wait_compare_gateway_activation(control, timeout=8):
    payload = _read_compare_auth_snapshot(control)
    generation = payload["generation"]
    control.wait_for_gateway_snapshot("auth", generation, timeout=timeout)
    return {"generation": generation, "records": len(payload["records"])}


def _validate_compare_compose(*args, check=True, capture=False):
    """Allow the immutable v1 policy path to perform validation without Docker.

    ``ControlPlane.update_account_policy`` invokes exactly ``compose config
    --quiet`` between rendering and Gateway activation.  Rendering is the
    comparison environment's actual validation boundary; every lifecycle or
    inspection command remains denied so this shim cannot become a Docker
    write escape hatch.
    """

    if args != ("config", "--quiet"):
        raise RuntimeError(
            "v1 comparison Compose command is not allowed: {}".format(" ".join(args))
        )
    del check
    return subprocess.CompletedProcess(
        args=("v1-compare-compose",) + args,
        returncode=0,
        stdout="" if capture else None,
        stderr="" if capture else None,
    )


def _reload_compare_gateway(control, timeout=8):
    """Confirm snapshot activation without testing or reloading a container."""

    return _wait_compare_gateway_activation(control, timeout=timeout)


def _apply_compare_changes(control, restart_containers=True, timeout=8):
    """Activate isolated v1 state without granting Docker write access.

    The immutable v1 Admin calls ``ControlPlane.apply_changes`` after user/Key
    mutations.  A normal production apply validates Compose and may recreate
    every business CPA, which is deliberately impossible behind the comparison
    topology's read-only Docker proxy.  The comparison-safe equivalent renders
    only its isolated files and waits until its own Gateway has activated the
    new auth-snapshot generation.

    This does not claim that a shared production CLIProxyAPI container reloaded
    the isolated generated account config.  That boundary is covered by the
    separate disposable data-plane rehearsal; the v1/main comparison Admin is
    allowed to mutate only its isolated root and its mounted Gateway snapshot.
    """

    del restart_containers
    control.render()
    return _wait_compare_gateway_activation(control, timeout=timeout)


def _require_compare_runtime():
    if os.environ.get("CLIPROXY_V1_COMPARE_MODE") != "1":
        raise RuntimeError("v1 comparison Admin requires CLIPROXY_V1_COMPARE_MODE=1")

    root = Path(os.environ.get("CLIPROXY_ROOT", "")).resolve()
    if not str(root) or root == Path("/"):
        raise RuntimeError("v1 comparison Admin requires one bounded CLIPROXY_ROOT")
    if not (root / ".v2-isolated-copy.json").is_file():
        raise RuntimeError("v1 comparison Admin requires an isolated-copy marker")

    docker_host = os.environ.get("DOCKER_HOST", "")
    socket_path = Path(
        os.environ.get("CLIPROXY_V1_COMPARE_DOCKER_READ_SOCKET", "")
    )
    if docker_host != "unix:///var/run/cpa-docker-read/docker.sock" or socket_path != Path(
        "/var/run/cpa-docker-read/docker.sock"
    ):
        raise RuntimeError("v1 comparison Admin requires the private Docker read endpoint")
    socket_stat = socket_path.stat() if socket_path.exists() else None
    if socket_stat is None or not stat.S_ISSOCK(socket_stat.st_mode):
        raise RuntimeError("v1 comparison Docker read endpoint is not a Unix socket")
    expected_project = os.environ.get(
        "CLIPROXY_V1_COMPARE_LIVE_COMPOSE_PROJECT", ""
    ).strip()
    if not expected_project:
        raise RuntimeError("v1 comparison live Compose project is missing")
    status, payload = _docker_read_request(socket_path, "GET", "/containers/json?all=1")
    if status != 200:
        raise RuntimeError("v1 comparison Docker read endpoint is unavailable")
    try:
        containers = json.loads(payload)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError("v1 comparison Docker read response is invalid") from error
    _normalize_container_rows(containers, expected_project)
    denied, _ = _docker_read_request(socket_path, "POST", "/containers/create")
    if denied != 403:
        raise RuntimeError("v1 comparison Docker read endpoint permits mutations")
    return root, socket_path, expected_project


def main(argv=None):
    _, docker_read_socket, live_compose_project = _require_compare_runtime()
    application_root = Path(
        os.environ.get("CLIPROXY_APP_ROOT", "/opt/codex-cpa-runtime")
    ).resolve()
    sys.path.insert(0, str(application_root / "admin"))

    from server import (  # pylint: disable=import-error,import-outside-toplevel
        AdminApplication,
        AdminHTTPServer,
        build_parser,
    )

    args = build_parser().parse_args(argv)
    app = AdminApplication(args.root)
    app._load_compose_ps = lambda: _load_compare_compose_ps(  # pylint: disable=protected-access
        docker_read_socket, live_compose_project
    )
    app.control.apply_changes = lambda restart_containers=True: _apply_compare_changes(
        app.control,
        restart_containers=restart_containers,
    )
    app.control.compose = _validate_compare_compose
    app.control._reload_gateway_if_running = (  # pylint: disable=protected-access
        lambda: _reload_compare_gateway(app.control)
    )
    server = AdminHTTPServer((args.host, args.port), app)
    print(
        "CPA v1 comparison Admin listening on {}:{}; autonomous schedulers disabled".format(
            args.host, args.port
        ),
        flush=True,
    )
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
