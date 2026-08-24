#!/usr/bin/env python3
"""Generate and verify the source-grounded v1 to Go v2 HTTP route matrix."""

import argparse
import ast
import json
import re
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MAP_PATH = ROOT / "api" / "migration-route-map.json"
DOC_PATH = ROOT / "docs" / "migration-api-matrix.md"
V1_SOURCE = ROOT / "admin" / "server.py"
GO_SOURCES = (
    ROOT / "internal" / "admin" / "server.go",
    ROOT / "internal" / "portal" / "server.go",
)
OPENAPI_PATH = ROOT / "api" / "openapi.yaml"


def route_key(method, path):
    return "{} {}".format(method.upper(), path)


class V1RouteVisitor(ast.NodeVisitor):
    def __init__(self):
        self.method = ""
        self.routes = set()

    def visit_FunctionDef(self, node):
        previous = self.method
        if node.name in {"do_GET", "do_POST", "do_PUT", "do_DELETE", "do_PATCH"}:
            self.method = node.name[3:]
        self.generic_visit(node)
        self.method = previous

    def visit_Compare(self, node):
        if self.method and len(node.ops) == 1 and isinstance(node.ops[0], ast.Eq):
            for candidate in (node.left, *node.comparators):
                if (
                    isinstance(candidate, ast.Constant)
                    and isinstance(candidate.value, str)
                    and candidate.value.startswith("/")
                ):
                    self.routes.add(route_key(self.method, candidate.value))
        self.generic_visit(node)


def extract_v1_routes(path=V1_SOURCE):
    visitor = V1RouteVisitor()
    visitor.visit(ast.parse(path.read_text(encoding="utf-8")))
    visitor.routes.discard("GET /admin")
    return visitor.routes


def extract_go_routes(paths=GO_SOURCES):
    routes = set()
    for path in paths:
        source = path.read_text(encoding="utf-8")
        prefixes = {"router": "", "server.router": ""}
        for _unused in range(8):
            changed = False
            for match in re.finditer(
                r'(\w+)\s*:=\s*([\w.]+)\.Group\("([^"]*)"\)', source
            ):
                name, base, suffix = match.groups()
                if base in prefixes and prefixes.get(name) != prefixes[base] + suffix:
                    prefixes[name] = prefixes[base] + suffix
                    changed = True
            if not changed:
                break
        for match in re.finditer(
            r'([\w.]+)\.(GET|POST|PUT|DELETE|PATCH)\("([^"]+)"', source
        ):
            base, method, route = match.groups()
            if base in prefixes:
                routes.add(route_key(method, prefixes[base] + route))
    return routes


def extract_openapi_routes(path=OPENAPI_PATH):
    routes = set()
    current = ""
    for line in path.read_text(encoding="utf-8").splitlines():
        path_match = re.match(r"^  (/[^:]+):\s*$", line)
        if path_match:
            current = path_match.group(1)
            continue
        method_match = re.match(r"^    (get|post|put|delete|patch):\s*$", line)
        if current and method_match:
            route = re.sub(r"\{([^}]+)\}", r":\1", current)
            routes.add(route_key(method_match.group(1), route))
    return routes


def load_mapping(path=MAP_PATH):
    payload = json.loads(path.read_text(encoding="utf-8"))
    if payload.get("version") != 1:
        raise ValueError("migration route map version must be 1")
    return payload


def build_rows(v1_routes, go_routes, mapping):
    aliases = mapping.get("aliases", {})
    removals = mapping.get("intentional_removals", {})
    unknown = (set(aliases) | set(removals)) - v1_routes
    if unknown:
        raise ValueError("mapping contains unknown v1 routes: {}".format(", ".join(sorted(unknown))))

    rows = []
    for source in sorted(v1_routes, key=lambda item: (item.split(" ", 1)[1], item)):
        if source in go_routes:
            rows.append({"source": source, "status": "exact", "targets": [source], "note": ""})
            continue
        if source in aliases:
            targets = aliases[source].get("targets", [])
            missing_targets = [target for target in targets if target not in go_routes]
            if not targets or missing_targets:
                raise ValueError(
                    "alias {} has unavailable Go targets: {}".format(
                        source, ", ".join(missing_targets or ["<empty>"])
                    )
                )
            rows.append(
                {
                    "source": source,
                    "status": "mapped",
                    "targets": targets,
                    "note": str(aliases[source].get("note", "")),
                }
            )
            continue
        if source in removals:
            rows.append(
                {
                    "source": source,
                    "status": "removed",
                    "targets": [],
                    "note": str(removals[source]),
                }
            )
            continue
        rows.append({"source": source, "status": "missing", "targets": [], "note": ""})
    return rows


def render_markdown(rows, go_routes, openapi_routes):
    status_label = {
        "exact": "原路径已实现",
        "mapped": "能力已拆分/合并",
        "removed": "明确移除",
        "missing": "尚未迁移",
    }
    counts = {name: sum(row["status"] == name for row in rows) for name in status_label}
    undocumented = sorted(
        route
        for route in go_routes - openapi_routes
        if route not in {"GET /admin/healthz"}
    )
    lines = [
        "# v1 到 Go v2 API 迁移矩阵",
        "",
        "本文件由 `python3 scripts/migration-route-matrix.py --write` 从当前 Python、Gin、OpenAPI 与",
        "`api/migration-route-map.json` 生成。不要手工维护表格内容。",
        "",
        "## 当前结论",
        "",
        "| 原路径已实现 | 能力拆分/合并 | 明确移除 | 尚未迁移 | Go 已注册 | OpenAPI 已记录 |",
        "| ---: | ---: | ---: | ---: | ---: | ---: |",
        "| {exact} | {mapped} | {removed} | {missing} | {go} | {openapi} |".format(
            go=len(go_routes), openapi=len(openapi_routes), **counts
        ),
        "",
        "`尚未迁移` 必须归零后，才能把“所有接口都已迁移”作为完成结论。`明确移除` 只允许记录已有",
        "产品决策；不能用来掩盖实现缺口。",
        "",
        "## v1 路由逐项追踪",
        "",
        "| v1 方法与路径 | 状态 | Go v2 方法与路径 | 说明 |",
        "| --- | --- | --- | --- |",
    ]
    for row in rows:
        targets = "<br>".join("`{}`".format(item) for item in row["targets"]) or "—"
        note = row["note"].replace("|", "\\|") or "—"
        lines.append(
            "| `{}` | {} | {} | {} |".format(
                row["source"], status_label[row["status"]], targets, note
            )
        )
    lines.extend(
        [
            "",
            "## Go 已注册但 OpenAPI 未记录",
            "",
        ]
    )
    if undocumented:
        lines.extend("- `{}`".format(route) for route in undocumented)
    else:
        lines.append("- 无")
    lines.extend(
        [
            "",
            "## 验证",
            "",
            "```bash",
            "python3 scripts/migration-route-matrix.py --check",
            "python3 scripts/migration-route-matrix.py --require-complete",
            "```",
            "",
            "第一条检查源码、映射与本文同步；第二条额外要求 API 缺口和 OpenAPI 漏项都归零。",
            "",
        ]
    )
    return "\n".join(lines), undocumented


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--write", action="store_true", help="write the generated Markdown matrix")
    parser.add_argument("--check", action="store_true", help="fail when the generated matrix drifted")
    parser.add_argument(
        "--require-complete",
        action="store_true",
        help="also fail while any v1 route is missing or a Go route lacks OpenAPI",
    )
    args = parser.parse_args(argv)
    v1_routes = extract_v1_routes()
    go_routes = extract_go_routes()
    openapi_routes = extract_openapi_routes()
    rows = build_rows(v1_routes, go_routes, load_mapping())
    rendered, undocumented = render_markdown(rows, go_routes, openapi_routes)
    if args.write:
        DOC_PATH.write_text(rendered, encoding="utf-8")
    if args.check or args.require_complete:
        if not DOC_PATH.is_file() or DOC_PATH.read_text(encoding="utf-8") != rendered:
            raise SystemExit("migration API matrix is stale; run --write")
    missing = [row["source"] for row in rows if row["status"] == "missing"]
    result = {
        "v1_routes": len(v1_routes),
        "go_routes": len(go_routes),
        "openapi_routes": len(openapi_routes),
        "exact": sum(row["status"] == "exact" for row in rows),
        "mapped": sum(row["status"] == "mapped" for row in rows),
        "removed": sum(row["status"] == "removed" for row in rows),
        "missing": missing,
        "go_without_openapi": undocumented,
    }
    print(json.dumps(result, ensure_ascii=False, indent=2, sort_keys=True))
    if args.require_complete and (missing or undocumented):
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
