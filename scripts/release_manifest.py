#!/usr/bin/env python3
"""Build and verify deterministic release component manifests."""

import argparse
import hashlib
import json
import os
import re
import stat
from pathlib import Path


MANIFEST_VERSION = 4
RELEASE_DESCRIPTOR_VERSION = 1
SEMVER_PATTERN = re.compile(r"^v?\d+\.\d+\.\d+(?:[-.][0-9A-Za-z.-]+)?$")
COMPONENT_INPUTS = {
    "admin": (".dockerignore", "admin", "scripts"),
    # Web serves the Admin frontend directly, so its image digest must change
    # whenever one of those copied static assets changes.
    "web": (".dockerignore", "admin/static", "dashboard", "portal", "web"),
    "gateway": (
        ".dockerignore",
        "gateway/Dockerfile",
        "gateway/gateway_state.lua",
        "gateway/nginx.conf",
        "gateway/request_gate.lua",
    ),
    "edge": (".dockerignore", "edge"),
    # Go v2 candidate images are published alongside the active v1 images, but
    # are not consumed by the production deployment script. Their independent
    # source identities let Test pull and compare an exact candidate build.
    "v2-control": (
        ".dockerignore",
        "go.mod",
        "go.sum",
        "v2/Dockerfile",
        "cmd/admin",
        "cmd/collector",
        "cmd/docker-read-proxy",
        "cmd/failover",
        "cmd/log-maintenance",
        "cmd/migration-compare",
        "cmd/notifications",
        "cmd/ownership",
        "cmd/quota",
        "internal/accountlifecycle",
        "internal/accountprojection",
        "internal/accountstatus",
        "internal/admin",
        "internal/branding",
        "internal/collector",
        "internal/contract",
        "internal/controlplane",
        "internal/dockerreadproxy",
        "internal/failover",
        "internal/gateway",
        "internal/identity",
        "internal/logmaintenance",
        "internal/migrationcheck",
        "internal/notifications",
        "internal/ownership",
        "internal/portal",
        "internal/quota",
        "internal/runtimeops",
        "internal/scheduler",
        "internal/usage",
    ),
    "v2-web": (
        ".dockerignore",
        "go.mod",
        "go.sum",
        "v2/Dockerfile",
        "cmd/web",
        "internal/web",
        "frontend/README.md",
        "frontend/index.html",
        "frontend/package.json",
        "frontend/package-lock.json",
        "frontend/portal",
        "frontend/scripts",
        "frontend/src",
        "frontend/tsconfig.json",
        "frontend/usage",
        "frontend/vite.config.ts",
        "frontend/vite.portal.config.ts",
        "frontend/vite.usage.config.ts",
    ),
    "v2-gateway": (
        ".dockerignore",
        "go.mod",
        "go.sum",
        "v2/Dockerfile",
        "cmd/gateway",
        "internal/gateway",
    ),
    "v2-edge": (
        ".dockerignore",
        "go.mod",
        "go.sum",
        "v2/Dockerfile",
        "cmd/edge",
        "internal/edge",
    ),
}
IGNORED_NAMES = {"__pycache__"}


def _included_files(root, inputs):
    files = []
    for relative in inputs:
        candidate = root / relative
        if not candidate.exists() and not candidate.is_symlink():
            raise ValueError("release component input is missing: {}".format(relative))
        if candidate.is_dir():
            for path in candidate.rglob("*"):
                nested = path.relative_to(root)
                if any(part in IGNORED_NAMES for part in nested.parts):
                    continue
                if path.is_file() or path.is_symlink():
                    if path.suffix == ".pyc":
                        continue
                    files.append(nested)
        else:
            files.append(Path(relative))
    return sorted(set(files), key=lambda item: item.as_posix())


def component_digest(root, inputs):
    root = Path(root).resolve()
    digest = hashlib.sha256()
    for relative in _included_files(root, inputs):
        path = root / relative
        metadata = path.lstat()
        mode = stat.S_IMODE(metadata.st_mode)
        if path.is_symlink():
            kind = "symlink"
            payload = os.readlink(path).encode("utf-8")
        elif path.is_file():
            kind = "file"
            payload = path.read_bytes()
        else:
            raise ValueError("unsupported release component input: {}".format(relative))
        header = "{}\0{}\0{:04o}\0{}\0".format(
            kind,
            relative.as_posix(),
            mode,
            len(payload),
        ).encode("utf-8")
        digest.update(header)
        digest.update(payload)
        digest.update(b"\0")
    return digest.hexdigest()


def build_manifest(root):
    root = Path(root).resolve()
    return {
        "version": MANIFEST_VERSION,
        "components": {
            component: {
                "source_sha256": component_digest(root, inputs),
                "inputs": list(inputs),
            }
            for component, inputs in COMPONENT_INPUTS.items()
        },
    }


def load_manifest(path):
    try:
        payload = json.loads(Path(path).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise ValueError("release manifest cannot be read: {}".format(error)) from error
    if not isinstance(payload, dict) or payload.get("version") != MANIFEST_VERSION:
        raise ValueError("unsupported release manifest version")
    components = payload.get("components")
    if not isinstance(components, dict):
        raise ValueError("release manifest components are missing")
    for component, inputs in COMPONENT_INPUTS.items():
        record = components.get(component)
        if not isinstance(record, dict):
            raise ValueError("release manifest component is missing: {}".format(component))
        if record.get("inputs") != list(inputs):
            raise ValueError("release manifest inputs do not match: {}".format(component))
        value = record.get("source_sha256")
        if not isinstance(value, str) or len(value) != 64:
            raise ValueError("release manifest digest is invalid: {}".format(component))
        try:
            int(value, 16)
        except ValueError as error:
            raise ValueError(
                "release manifest digest is invalid: {}".format(component)
            ) from error
    return payload


def verify_manifest(root, path):
    expected = load_manifest(path)
    actual = build_manifest(root)
    if expected != actual:
        mismatches = [
            component
            for component in COMPONENT_INPUTS
            if expected["components"][component]
            != actual["components"][component]
        ]
        raise ValueError(
            "release manifest verification failed: {}".format(", ".join(mismatches))
        )
    return actual


def _write_json(payload, output):
    output = Path(output)
    output.parent.mkdir(parents=True, exist_ok=True)
    temporary = output.with_name(".{}.{}.tmp".format(output.name, os.getpid()))
    temporary.write_text(
        json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.chmod(temporary, 0o644)
    os.replace(temporary, output)


def write_manifest(root, output):
    _write_json(build_manifest(root), output)


def build_release_descriptor(
    root, release_version, revision, image_prefix, archive_name
):
    """Describe the exact component source identities shipped by one release."""
    release_version = str(release_version).strip()
    revision = str(revision).strip().lower()
    image_prefix = str(image_prefix).strip().rstrip("/")
    archive_name = str(archive_name).strip()
    if SEMVER_PATTERN.fullmatch(release_version) is None:
        raise ValueError("release version is invalid")
    if len(revision) < 7 or any(
        character not in "0123456789abcdef" for character in revision
    ):
        raise ValueError("release revision is invalid")
    if not image_prefix or "/" not in image_prefix:
        raise ValueError("release image prefix is invalid")
    if not archive_name or Path(archive_name).name != archive_name:
        raise ValueError("release archive name is invalid")

    manifest = build_manifest(root)
    components = {}
    for component, record in manifest["components"].items():
        source_digest = record["source_sha256"]
        components[component] = {
            "image": "{}/codex-cpa-{}:sha256-{}".format(
                image_prefix, component, source_digest
            ),
            "source_sha256": source_digest,
        }
    return {
        "schema_version": RELEASE_DESCRIPTOR_VERSION,
        "release_version": release_version,
        "revision": revision,
        "archive": {"name": archive_name},
        "components": components,
        # Current Admin versions still consume this compatibility image.
        "metadata_image": "{}/codex-cpa-release:{}".format(
            image_prefix, release_version
        ),
    }


def write_release_descriptor(
    root, output, release_version, revision, image_prefix, archive_name
):
    _write_json(
        build_release_descriptor(
            root, release_version, revision, image_prefix, archive_name
        ),
        output,
    )


def build_parser():
    parser = argparse.ArgumentParser(description="创建或校验发布组件指纹")
    subparsers = parser.add_subparsers(dest="command", required=True)

    create = subparsers.add_parser("create")
    create.add_argument("--root", required=True)
    create.add_argument("--output", required=True)

    verify = subparsers.add_parser("verify")
    verify.add_argument("--root", required=True)
    verify.add_argument("--manifest", required=True)

    digest = subparsers.add_parser("digest")
    digest.add_argument("--root", required=True)
    digest.add_argument("--component", choices=tuple(COMPONENT_INPUTS), required=True)

    release = subparsers.add_parser("release")
    release.add_argument("--root", required=True)
    release.add_argument("--output", required=True)
    release.add_argument("--release-version", required=True)
    release.add_argument("--revision", required=True)
    release.add_argument("--image-prefix", required=True)
    release.add_argument("--archive-name", required=True)
    return parser


def main(argv=None):
    args = build_parser().parse_args(argv)
    try:
        if args.command == "create":
            write_manifest(args.root, args.output)
        elif args.command == "verify":
            verify_manifest(args.root, args.manifest)
        elif args.command == "digest":
            print(component_digest(args.root, COMPONENT_INPUTS[args.component]))
        else:
            write_release_descriptor(
                args.root,
                args.output,
                args.release_version,
                args.revision,
                args.image_prefix,
                args.archive_name,
            )
    except ValueError as error:
        raise SystemExit(str(error)) from error


if __name__ == "__main__":
    main()
