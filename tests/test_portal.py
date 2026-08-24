import colorsys
import re
import shlex
import subprocess
import tempfile
import unittest
from html.parser import HTMLParser
from pathlib import Path


ROOT = Path(__file__).parents[1]


class LinkCollector(HTMLParser):
    def __init__(self):
        super().__init__()
        self.links = []

    def handle_starttag(self, tag, attrs):
        if tag == "a":
            href = dict(attrs).get("href")
            if href:
                self.links.append(href)


class PortalTests(unittest.TestCase):
    def test_frontend_sources_have_no_green_or_teal_color_literals(self):
        source_paths = (
            "admin/static/app.css",
            "admin/static/app.js",
            "portal/app.css",
            "scripts/admin-preview.py",
            "scripts/cliproxy.py",
            "scripts/control_plane_store.py",
            "admin/server.py",
        )
        color_pattern = re.compile(
            r"#([0-9a-fA-F]{6})(?:[0-9a-fA-F]{2})?\b|rgba?\(([^)]*)\)"
        )
        offenders = []
        for relative_path in source_paths:
            lines = (ROOT / relative_path).read_text(encoding="utf-8").splitlines()
            for line_number, line in enumerate(lines, 1):
                for match in color_pattern.finditer(line):
                    if match.group(1):
                        value = match.group(1)
                        red, green, blue = (
                            int(value[index:index + 2], 16) / 255.0
                            for index in (0, 2, 4)
                        )
                    else:
                        channels = re.findall(r"[\d.]+", match.group(2))
                        if len(channels) < 3:
                            continue
                        red, green, blue = (
                            float(channel) / 255.0 for channel in channels[:3]
                        )
                    hue, lightness, saturation = colorsys.rgb_to_hls(
                        red,
                        green,
                        blue,
                    )
                    hue *= 360
                    if (
                        65 <= hue <= 195
                        and saturation >= 0.05
                        and 0.05 <= lightness <= 0.98
                    ):
                        offenders.append(
                            "{}:{} {}".format(
                                relative_path,
                                line_number,
                                match.group(0),
                            )
                        )
        self.assertEqual(offenders, [], "发现绿色或青绿色界面色值")

    def test_deploy_release_creates_portable_empty_backup_archive(self):
        deploy = (ROOT / "scripts" / "deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("create_empty_archive() {")
        end = deploy.index("\nset --", start)
        helper = deploy[start:end]

        with tempfile.TemporaryDirectory() as temporary:
            archive = Path(temporary) / "empty.tar.gz"
            script = "{}\ncreate_empty_archive {}\n".format(
                helper,
                shlex.quote(str(archive)),
            )
            subprocess.run(["sh"], input=script, text=True, check=True)
            listed = subprocess.run(
                ["tar", "-tzf", str(archive)],
                text=True,
                capture_output=True,
                check=True,
            )

            self.assertEqual(listed.stdout, "")
            self.assertNotIn("--files-from /dev/null", deploy)

    def test_deploy_release_persists_one_canonical_deploy_root(self):
        deploy = (ROOT / "scripts" / "deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("write_deploy_root_env() {")
        end = deploy.index("\ncompose()", start)
        helper = deploy[start:end]

        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "runtime"
            target.mkdir()
            env = target / ".env"
            env.write_text(
                "GATEWAY_PORT=18317\n"
                "DEPLOY_ROOT=/opt/old-root\n"
                " export DEPLOY_ROOT=/opt/duplicate-root\n"
                "ADMIN_IMAGE=admin:test\n",
                encoding="utf-8",
            )
            script = "{}\nTARGET={}\nwrite_deploy_root_env\n".format(
                helper,
                shlex.quote(str(target)),
            )
            subprocess.run(["sh"], input=script, text=True, check=True)

            lines = env.read_text(encoding="utf-8").splitlines()
            self.assertEqual(lines.count("DEPLOY_ROOT={}".format(target)), 1)
            self.assertEqual(env.stat().st_mode & 0o777, 0o600)
            self.assertIn("GATEWAY_PORT=18317", lines)
            self.assertIn("ADMIN_IMAGE=admin:test", lines)

    def test_application_build_base_images_are_digest_pinned(self):
        values = {}
        for line in (ROOT / "compose.env.example").read_text(encoding="utf-8").splitlines():
            if "=" in line:
                key, value = line.split("=", 1)
                values[key] = value

        for key in (
            "ADMIN_BASE_IMAGE",
            "GATEWAY_IMAGE",
            "EDGE_IMAGE",
        ):
            self.assertRegex(values[key], r"^[^@]+@sha256:[0-9a-f]{64}$")

        for relative, argument in (
            ("admin/Dockerfile", "ADMIN_BASE_IMAGE"),
            ("web/Dockerfile", "WEB_BASE_IMAGE"),
            ("gateway/Dockerfile", "GATEWAY_BASE_IMAGE"),
            ("edge/Dockerfile", "EDGE_BASE_IMAGE"),
        ):
            dockerfile = (ROOT / relative).read_text(encoding="utf-8")
            prefix = "ARG {}=".format(argument)
            reference = next(
                line[len(prefix):]
                for line in dockerfile.splitlines()
                if line.startswith(prefix)
            )
            self.assertRegex(reference, r"^[^@]+@sha256:[0-9a-f]{64}$")

        deploy = (ROOT / "scripts" / "deploy-release.sh").read_text(encoding="utf-8")
        self.assertNotIn("docker build \\", deploy)
        self.assertNotIn("resolve_base_image_id", deploy)
        self.assertIn('docker pull "$RELEASE_VERSION_IMAGE"', deploy)

    def test_codex_cpa_cluster_brand_is_consistent_across_surfaces(self):
        portal = (ROOT / "portal" / "index.html").read_text(encoding="utf-8")
        usage = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        native = (ROOT / "portal" / "native.html").read_text(encoding="utf-8")
        admin = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")
        logo = ROOT / "portal" / "assets" / "codex-cpa-cluster-logo.svg"
        dark_logo = ROOT / "portal" / "assets" / "codex-cpa-cluster-logo-dark.svg"
        mark = ROOT / "portal" / "assets" / "codex-cpa-cluster-mark.svg"
        dark_mark = ROOT / "portal" / "assets" / "codex-cpa-cluster-mark-dark.svg"
        favicon = ROOT / "portal" / "assets" / "codex-cpa-cluster-favicon.svg"
        dark_favicon = ROOT / "portal" / "assets" / "codex-cpa-cluster-favicon-dark.svg"

        for html in (portal, usage, native, admin):
            self.assertIn("Codex CPA Cluster", html)
            self.assertIn('/portal/assets/codex-cpa-cluster-favicon.svg', html)
            self.assertIn('type="image/svg+xml"', html)
        self.assertIn('/portal/assets/codex-cpa-cluster-logo.svg', portal)
        self.assertIn('/portal/assets/codex-cpa-cluster-logo.svg', usage)
        self.assertIn('/portal/assets/codex-cpa-cluster-logo.svg', admin)
        self.assertIn('/portal/assets/codex-cpa-cluster-mark.svg', admin)
        self.assertIn("state.siteConfig.product_name", script)
        self.assertTrue(logo.is_file())
        self.assertTrue(dark_logo.is_file())
        self.assertTrue(mark.is_file())
        self.assertTrue(dark_mark.is_file())
        self.assertTrue(favicon.is_file())
        self.assertTrue(dark_favicon.is_file())
        self.assertIn("CPA CLUSTER", logo.read_text(encoding="utf-8"))
        self.assertIn("#6374D8", logo.read_text(encoding="utf-8"))
        self.assertIn("#8D9BF1", dark_logo.read_text(encoding="utf-8"))

    def test_root_portal_exposes_every_ui_destination(self):
        parser = LinkCollector()
        parser.feed((ROOT / "portal" / "index.html").read_text(encoding="utf-8"))

        self.assertEqual(
            set(parser.links),
            {"/admin/", "/usage/"},
        )
        html = (ROOT / "portal" / "index.html").read_text(encoding="utf-8")
        self.assertIn("使用中心", html)
        self.assertNotIn("CPA 使用中心", html)
        self.assertNotIn('href="/my-keys/"', html)
        self.assertNotIn("Token Usage", html)
        self.assertNotIn("API ACCESS", html)
        self.assertNotIn("<h2>业务 CPA</h2>", html)

    def test_native_selector_only_exposes_business_cpas_and_admin_add_action(self):
        parser = LinkCollector()
        parser.feed((ROOT / "portal" / "native.html").read_text(encoding="utf-8"))

        self.assertEqual(
            set(parser.links),
            {
                "/",
                "/admin/?action=add-account",
            },
        )
        html = (ROOT / "portal" / "native.html").read_text(encoding="utf-8")
        script = (ROOT / "portal" / "native.js").read_text(encoding="utf-8")
        self.assertNotIn("统一管理 CPA", html)
        self.assertIn(".native-error[hidden]", (ROOT / "portal" / "app.css").read_text(encoding="utf-8"))
        self.assertIn('fetch("/admin/api/native-accounts"', script)
        self.assertIn('credentials: "same-origin"', script)
        self.assertIn("account.management_url", script)
        self.assertNotIn("window.location.hostname", script)
        self.assertNotIn("account.port", script)

    def test_edge_web_and_gateway_have_separate_route_ownership(self):
        gateway = (ROOT / "gateway" / "nginx.conf").read_text(encoding="utf-8")
        edge = (ROOT / "edge" / "nginx.conf").read_text(encoding="utf-8")
        web = (ROOT / "web" / "nginx.conf").read_text(encoding="utf-8")
        api_catch_all = gateway.rindex("location / {")
        allowed_routes = (
            "~^/v1(?:/|$) 1;",
            "~^/v1beta(?:/|$) 1;",
            "~^/backend-api/codex(?:/|$) 1;",
            "~^/api/provider(?:/|$) 1;",
            "~^/v1internal:method$ 1;",
        )

        self.assertIn("try_files /portal/index.html =404", web)
        self.assertIn("try_files /portal/native.html =404", web)
        native_accounts_route = web[
            web.index("location = /native/accounts.json"):
            web.index("location = /my-keys")
        ]
        self.assertIn("return 404;", native_accounts_route)
        self.assertNotIn("alias", native_accounts_route)
        self.assertIn('require("gateway_state").start()', gateway)
        self.assertIn("user nobody;", gateway)
        for route in allowed_routes:
            self.assertIn(route, gateway)
        self.assertEqual(gateway.count("map $uri $public_api_route_allowed"), 1)
        self.assertIn("if ($public_api_route_allowed = 0)", gateway)
        for sensitive_route in (
            "/v0/management",
            "/management.html",
            "/healthz",
            "/codex/callback",
            "/google/callback",
        ):
            allowlist_start = gateway.index("map $uri $public_api_route_allowed")
            allowlist_end = gateway.index("server {", allowlist_start)
            self.assertNotIn(sensitive_route, gateway[allowlist_start:allowlist_end])
        self.assertIn('require("request_gate").authorize()', gateway[api_catch_all:])
        self.assertIn("proxy_set_header Authorization $upstream_authorization;", gateway)
        self.assertNotIn("/portal/", gateway)
        self.assertNotIn("/admin/", gateway)
        self.assertNotIn("/usage/limits", gateway)
        self.assertIn("location = /__internal/probe/models", gateway)
        probe = gateway[
            gateway.index("location = /__internal/probe/models"):
            gateway.index("location = /__internal/snapshots")
        ]
        self.assertIn("access_log off;", probe)
        self.assertIn("proxy_pass http://$backend/v1/models;", probe)
        self.assertNotIn("cpa_stats", probe)
        self.assertNotIn("if ($key_label", probe)
        self.assertIn("listen 8319;", gateway)
        self.assertIn("location ^~ /__internal/", gateway)
        self.assertIn('ngx.header["Strict-Transport-Security"] = "max-age=0"', gateway)
        self.assertNotIn("max-age=31536000", gateway)
        public_api = gateway[api_catch_all:]
        self.assertIn("access_by_lua_block", public_api)
        self.assertIn("log_by_lua_block", public_api)
        self.assertIn(
            "include /var/run/cliproxy-edge/active-gateway.conf;", edge
        )
        self.assertIn("gateway-blue:8317 gateway-blue:8319;", edge)
        self.assertIn("gateway-green:8317 gateway-green:8319;", edge)
        self.assertIn("proxy_pass http://$active_gateway_internal_backend;", edge)
        self.assertNotIn("/var/run/cliproxy-edge/*.conf", edge)
        self.assertIn("location ^~ /admin/ { proxy_pass http://$web_backend; }", edge)
        self.assertIn("location / {", edge)
        self.assertIn("proxy_pass http://$active_gateway_backend;", edge)
        self.assertNotIn("request_gate", edge)
        self.assertNotIn("Authorization $upstream_authorization", edge)
        self.assertIn('proxy_set_header X-Management-Key "";', web)
        self.assertIn('proxy_set_header Authorization "";', web)
        self.assertIn("frame-ancestors 'none'", web)
        self.assertNotIn("unsafe-inline", web)

    def test_gateway_weekly_quota_gate_is_soft_fail_open_and_release_is_complete(self):
        gate = (ROOT / "gateway" / "request_gate.lua").read_text(encoding="utf-8")
        state = (ROOT / "gateway" / "gateway_state.lua").read_text(encoding="utf-8")
        package = (ROOT / "scripts" / "package-release.sh").read_text(encoding="utf-8")
        deploy = (ROOT / "scripts" / "deploy-release.sh").read_text(encoding="utf-8")
        compose = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
        dev_compose = (ROOT / "docker-compose.dev.yml").read_text(encoding="utf-8")
        admin_dockerfile = (ROOT / "admin" / "Dockerfile").read_text(encoding="utf-8")
        gateway_dockerfile = (ROOT / "gateway" / "Dockerfile").read_text(encoding="utf-8")

        self.assertIn("if limit >= 0 and used >= limit then", gate)
        self.assertIn("weekly_user_token_quota_exceeded", gate)
        self.assertIn("last_success_at", gate)
        self.assertIn("now - last_success_at > fail_open_after", gate)
        self.assertIn('warn_fail_open("snapshot_period_expired")', gate)
        self.assertIn("AUTH_LOADER_MAX_AGE_SECONDS", gate)
        self.assertIn("now - auth_loader_success_at >", gate)
        self.assertIn('require "resty.sha256"', gate)
        self.assertIn('require "resty.string"', gate)
        self.assertNotIn("ngx.sha256_bin", gate)
        self.assertNotIn("log_by_lua", gate)
        self.assertIn('dict:set("snapshot_loader_success_at", ngx.time())', state)
        self.assertIn('dict:set("last_success_at"', state)
        self.assertIn('dict:set("fail_open_after"', state)
        self.assertIn("gateway/gateway_state.lua", package)
        self.assertIn("gateway/request_gate.lua", package)
        self.assertIn("requirements.txt", package)
        self.assertIn("gateway/Dockerfile", package)
        self.assertIn("release-manifest.json", package)
        self.assertIn("release_manifest.py", package)
        self.assertIn("runtime_data_guard.py", deploy)
        self.assertIn("quarantine_legacy_source", deploy)
        self.assertIn("profile import-once --preserve-existing", deploy)
        self.assertIn("store cleanup-projections", deploy)
        self.assertIn("store migrate-secrets --cleanup", deploy)
        self.assertIn("write_deploy_root_env || return 1", deploy)
        self.assertIn('rendered.append("DEPLOY_ROOT={}".format(deploy_root))', deploy)
        self.assertIn("run_cli_in_image", deploy)
        self.assertIn("release_cli render", deploy)
        self.assertIn("--network host", deploy)
        self.assertNotIn('python3 "$APP_CLI" --root "$TARGET"', deploy)
        self.assertNotIn('"--force-recreate", service', deploy)
        self.assertIn("GATEWAY_APPLY_REQUIRED", deploy)
        self.assertIn("assert_data_plane_unchanged", deploy)
        self.assertIn('config --hash "$SERVICE"', deploy)
        self.assertIn("com.docker.compose.config-hash", deploy)
        self.assertIn('compose up -d --no-deps "$INACTIVE_GATEWAY_SERVICE"', deploy)
        self.assertIn("switch_gateway_slot", deploy)
        self.assertIn("drain_gateway_slot", deploy)
        self.assertIn(
            "admin usage-collector log-maintenance management",
            deploy,
        )
        self.assertNotIn(
            "compose stop admin usage-collector log-maintenance gateway",
            deploy,
        )
        self.assertIn("LICENSE", package)
        self.assertIn(
            "ADMIN_BASE_IMAGE: ${ADMIN_BASE_IMAGE:-docker.m.daocloud.io/library/docker:27.5.1-cli@sha256:851f91d241214e7c6db86513b270d58776379aacc5eb9c4a87e5b47115e3065c}",
            dev_compose,
        )
        self.assertIn("GATEWAY_RUNTIME_IMAGE:?state/compose.env missing", compose)
        self.assertNotIn("./gateway:/usr/local/openresty", compose)
        self.assertNotIn("./portal:/usr/local/openresty", compose)
        for service in ("gateway-blue", "gateway-green", "edge", "web"):
            self.assertIn("  {}:".format(service), compose)
        self.assertIn(
            "ARG ADMIN_BASE_IMAGE=docker.m.daocloud.io/library/docker:27.5.1-cli@sha256:851f91d241214e7c6db86513b270d58776379aacc5eb9c4a87e5b47115e3065c",
            admin_dockerfile,
        )
        self.assertIn("FROM ${ADMIN_BASE_IMAGE}", admin_dockerfile)
        self.assertIn("COPY admin ./admin", admin_dockerfile)
        self.assertIn("COPY scripts ./scripts", admin_dockerfile)
        self.assertNotIn("COPY portal ./portal", admin_dockerfile)
        self.assertIn(
            "apk add --no-cache --upgrade expat py3-cryptography python3 tzdata",
            admin_dockerfile,
        )
        self.assertIn("xml.etree.ElementTree", admin_dockerfile)
        self.assertIn("ARG GATEWAY_BASE_IMAGE=", gateway_dockerfile)
        self.assertNotIn("COPY portal", gateway_dockerfile)
        self.assertNotIn("COPY dashboard", gateway_dockerfile)
        self.assertIn("Python 缺少可用的 SQLite 或 XML 标准库", deploy)

    def test_release_stops_legacy_control_plane_writers_before_projection_cleanup(self):
        deploy = (ROOT / "scripts" / "deploy-release.sh").read_text(encoding="utf-8")

        stop = "compose stop admin usage-collector log-maintenance"
        cleanup = "release_cli store cleanup-projections"
        self.assertIn(stop, deploy)
        self.assertIn(cleanup, deploy)
        self.assertLess(deploy.index(stop), deploy.index(cleanup))

    def test_self_service_ui_uses_cookie_session_and_one_key(self):
        html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")
        style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        self.assertIn("我的 API Key", html)
        self.assertIn("个人用量", html)
        self.assertIn('id="user-quota-copy"', html)
        self.assertIn('class="usage-current-quota" id="user-quota"', html)
        self.assertIn('id="user-quota-raw"', html)
        self.assertIn('id="range-raw-tokens"', html)
        self.assertIn('id="range-weighted-tokens"', html)
        self.assertIn('id="range-request-count"', html)
        self.assertIn('id="usage-summary-label" aria-live="polite"', html)
        self.assertIn("今日 Token", html)
        self.assertNotIn("采集以来 · 全部 CPA", html)
        self.assertIn('<div class="usage-token-overview">', html)
        self.assertLess(
            html.index('<div class="usage-key-panel">'),
            html.index('<div class="usage-token-overview">'),
        )
        self.assertLess(
            html.index('<div class="usage-token-overview">'),
            html.index('<div class="usage-current-account"'),
        )
        self.assertIn('type="email"', html)
        self.assertIn('id="login-password"', html)
        self.assertIn('id="password-dialog"', html)
        self.assertIn('id="new-password"', html)
        self.assertIn("首次登录请修改密码", html)
        self.assertIn('id="change-password-button"', html)
        self.assertIn('aria-label="修改密码"', html)
        self.assertIn('id="cancel-password"', html)
        self.assertIn('id="password-notice"', html)
        self.assertIn('const enhancePasswordFields =', script)
        self.assertIn('input[type="password"]:not([data-password-input])', script)
        self.assertIn('button.setAttribute("aria-pressed", String(visible));', script)
        self.assertIn('visible ? "隐藏密码" : "显示密码"', script)
        self.assertIn('input.setAttribute("aria-label", fieldLabel);', script)
        self.assertIn('.password-visibility-toggle', style)
        self.assertIn('.password-input > input { padding-right: 46px; }', style)
        self.assertIn('fetch("/usage/session"', script)
        self.assertIn('fetch("/usage/me/password"', script)
        self.assertIn('showPasswordChange({ required: false })', script)
        self.assertIn('showPasswordChange({ required: true, currentPassword: password })', script)
        self.assertIn('$("#change-password-button").hidden = false;', script)
        self.assertIn('$("#change-password-button").hidden = true;', script)
        self.assertIn('state.passwordChangeRequired = Boolean(required);', script)
        self.assertIn('.usage-user-actions > [hidden] { display: none !important; }', style)
        self.assertIn('fetch(`/usage/me?window=${state.windowSeconds}&lifetime=0${freshQuery}`', script)
        self.assertIn('fetch(`/usage/me/usage-breakdown?${query.toString()}`', script)
        self.assertIn("group?.operational_status", script)
        self.assertIn("operational.selectable !== false", script)
        self.assertIn("我的模型 × 推理强度 Token 明细", script)
        self.assertIn("<strong>我的使用明细</strong>", script)
        self.assertNotIn("${escapeHTML(group.account)} · 我的使用明细", script)
        self.assertIn("groupAccountModelUsage", script)
        self.assertIn("accountModelEffortColorKey", script)
        self.assertIn("加权 Token：", script)
        self.assertIn("Date.now() - cached.fetchedAt < 30_000", script)
        self.assertIn("data-usage-breakdown-retry", script)
        self.assertIn("const resetUsageBreakdowns = () =>", script)
        self.assertIn("usageBreakdownControllers", script)
        self.assertIn("signal: controller.signal", script)
        self.assertIn('if (requestError.name === "AbortError") return;', script)
        self.assertIn('/admin/reasoning-effort-colors.css', html)
        self.assertLess(
            html.index('/admin/monitor-utils.js'),
            html.index('/portal/my-keys.js'),
        )
        self.assertIn('const freshQuery = fresh ? "&fresh=1" : "";', script)
        self.assertIn('loadDashboard({ fresh: true })', script)
        self.assertIn('fetch("/usage/me/group"', script)
        self.assertIn("renderUserQuota(payload.weekly_quota)", script)
        self.assertIn("renderUsageSummary(windowUsageForPayload(payload))", script)
        self.assertIn("const summarizeGroupUsage = (groups = []) =>", script)
        self.assertIn("const windowUsageForPayload = (payload) =>", script)
        self.assertIn("payload.window_usage", script)
        self.assertIn("summarizeGroupUsage(payload.groups)", script)
        self.assertIn("renderUsageSummary(null);", script)
        for label in ('3600: "1 小时"', 'today: "今日"', '86400: "24 小时"', '604800: "7 天"'):
            self.assertIn(label, script)
        self.assertIn('$("#usage-summary-label").textContent = `${rangeLabel} Token`;', script)
        self.assertIn('$("#range-request-count").textContent = `${rangeLabel}请求', script)
        self.assertIn("quota.weighted_used_tokens ?? used", script)
        self.assertIn("quota.raw_used_tokens", script)
        self.assertIn("`加权已用 ${formatTokenAmount(weightedUsed)}", script)
        self.assertIn("`未加权累计 ${formatTokenAmount(rawUsed)}`", script)
        self.assertNotIn("renderLifetimeUsage", script)
        self.assertIn('`周额度 ${percentCopy}%`', script)
        self.assertIn('`剩余 ${remainingCopy}%`', script)
        self.assertIn("TokenUsageFormatter.format(value)", script)
        self.assertIn('method: "POST"', script)
        self.assertIn("body: JSON.stringify({ email, password })", script)
        self.assertNotIn('"Authorization":', script)
        self.assertNotIn("'Authorization':", script)
        self.assertNotIn("Authorization: `Bearer", script)
        self.assertIn("复制 Key", html)
        self.assertIn('id="rotate-key" type="button">刷新 Key</button>', html)
        self.assertIn('id="rotate-key-dialog" role="alertdialog"', html)
        self.assertIn("旧 Key 会立即失效", html)
        self.assertIn('fetch("/usage/me/key/rotate"', script)
        self.assertIn("body: JSON.stringify({ confirm: true })", script)
        self.assertIn('state.payload = { ...state.payload, api_key: payload.api_key };', script)
        self.assertIn(".usage-key-rotation-warning", style)
        self.assertIn(".usage-danger-button", style)
        self.assertIn('id="open-codex-config" type="button">Codex</button>', html)
        self.assertNotIn('>config.toml</button>', html)
        self.assertIn('id="config-file"', html)
        self.assertIn('id="config-steps"', html)
        self.assertIn('id="config-external-link"', html)
        self.assertIn("操作文件", html)
        self.assertIn("操作步骤", html)
        self.assertIn('<table class="usage-account-table">', html)
        for column in (
            "当前账号",
            "CPA 账号",
            "账号周额度",
            "所有用户共享",
            "活跃用户",
            "近 1 小时",
            "账号状态",
            "我的请求",
            "我的 Token",
            "最后使用",
            "我的记录",
        ):
            self.assertIn(column, html)
        self.assertNotIn('href="/admin/"', html)
        self.assertNotIn("账号数据", html)
        self.assertNotIn("我的数据", html)
        self.assertNotIn("查询 Key", html)
        self.assertNotIn("账号使用情况", html)
        self.assertNotIn('role="tablist"', html)
        self.assertIn('id="user-badge"', html)
        self.assertIn('id="logout-button"', html)
        self.assertIn('id="login-dialog"', html)
        self.assertIn('method: "DELETE"', script)
        self.assertNotIn("allowed_email_domains", script)
        self.assertNotIn("domains.length === 1", script)
        self.assertNotIn('!value.includes("@")', script)
        self.assertIn('response.headers.get("Retry-After")', script)
        self.assertIn("startLoginRetryCountdown", script)
        self.assertIn("导入 CC Switch", html)
        self.assertIn("Claude Code", html)
        self.assertIn("buildClaudeCodeConfig", script)
        self.assertIn("buildClaudeCodeSections", script)
        self.assertIn("buildClaudeCodeLauncher", script)
        self.assertIn("buildClaudeCodeEnv", script)
        self.assertIn("ANTHROPIC_BASE_URL=${shellQuote(gatewayOrigin())}", script)
        self.assertIn('export ANTHROPIC_AUTH_TOKEN="$${keyVariable}"', script)
        self.assertIn('api_key_env: "CPA_API_KEY"', script)
        self.assertIn("ANTHROPIC_DEFAULT_OPUS_MODEL=${shellQuote(model)}", script)
        self.assertIn("ANTHROPIC_DEFAULT_SONNET_MODEL=${shellQuote(model)}", script)
        self.assertIn("ANTHROPIC_DEFAULT_HAIKU_MODEL=${shellQuote(model)}", script)
        self.assertIn('export CLAUDE_CODE_EFFORT_LEVEL="xhigh"', script)
        self.assertIn("--effort xhigh", script)
        self.assertIn("~/.config/claude-cpa/env", script)
        self.assertIn("~/.config/claude-cpa/claude-cpa.zsh", script)
        self.assertIn("仅在可信设备保存", script)
        self.assertIn('id="config-workflow"', html)
        self.assertIn('id="config-notice"', html)
        self.assertIn("~/.codex/config.toml", script)
        self.assertIn('title: "Codex 配置"', script)
        self.assertIn('title: "Claude Code 终端配置"', script)
        self.assertIn("重新启动 Codex", script)
        self.assertIn("加载并验证", script)
        self.assertIn('$("#config-file").textContent = file', script)
        self.assertIn('$("#config-steps").innerHTML = steps', script)
        self.assertIn("renderConfigSections(sections)", script)
        self.assertIn("code.textContent = section.value", script)
        self.assertIn("copyText(state.activeConfig)", script)
        self.assertIn('document.execCommand("copy")', script)
        self.assertIn('experimental_bearer_token = ${tomlString(state.payload?.api_key || "")}', script)
        self.assertIn("wire_api = \"responses\"", script)
        self.assertIn('requires_openai_auth = false', script)
        self.assertIn('http_headers = { \"X-OpenAI-Actor-Authorization\" = \"local-proxy\" }', script)
        self.assertIn('default_model: "gpt-5.6-sol"', script)
        self.assertIn('`model = ${tomlString(modelName())}`', script)
        self.assertIn('const DEFAULT_REASONING_EFFORT = "xhigh"', script)
        self.assertIn('const DEFAULT_PLAN_REASONING_EFFORT = "xhigh"', script)
        self.assertIn('model_reasoning_effort = ${tomlString(DEFAULT_REASONING_EFFORT)}', script)
        self.assertIn('plan_mode_reasoning_effort = ${tomlString(DEFAULT_PLAN_REASONING_EFFORT)}', script)
        self.assertNotIn('gpt-5.4', script)
        self.assertIn("ccswitch://v1/import?", script)
        self.assertIn('app: "codex"', script)
        self.assertIn('resource: "provider"', script)
        self.assertIn('apiKey: state.payload?.api_key || ""', script)
        self.assertIn('const browserOrigin = window.location.origin;', script)
        self.assertIn('public_base_url: ""', script)
        self.assertIn('const publicBaseUrl = () =>', script)
        self.assertIn('const url = new URL(configured || browserOrigin);', script)
        self.assertIn('["http:", "https:"].includes(url.protocol)', script)
        self.assertIn('const gatewayOrigin = () => publicBaseUrl();', script)
        self.assertIn('const gatewayBaseUrl = () => `${gatewayOrigin()}/v1`;', script)
        self.assertIn("endpoint: gatewayBaseUrl()", script)
        self.assertIn('homepage: `${gatewayOrigin()}/usage/`', script)
        self.assertIn("value: buildCodexConfig()", script)
        self.assertIn("const buildCodexConfig = (baseUrl = gatewayBaseUrl()) =>", script)
        self.assertNotIn('replace(/^https:/, "http:")', script)
        self.assertIn("完成 CC Switch 图片配置", script)
        self.assertIn("将已复制的内容完整替换到 config.toml", script)
        self.assertIn("无需开启 CC Switch 本地路由", script)
        self.assertIn("复制配置并继续导入", script)
        self.assertIn("event.preventDefault()", script)
        self.assertIn("externalLink: buildCcSwitchLink()", script)
        self.assertIn("导入后按使用中心提示粘贴完整 config.toml 以启用图片生成", script)
        self.assertIn('data-switch-group=', script)
        self.assertIn('data-expand-group=', script)
        self.assertIn("输入 Token", script)
        self.assertIn("推理 Token", script)
        self.assertIn('const THEME_STORAGE_KEY = "cpa-ui-theme"', script)
        self.assertIn('window.localStorage.setItem(THEME_STORAGE_KEY, resolved)', script)
        self.assertNotIn('localStorage.setItem("api_key"', script)
        self.assertNotIn("sessionStorage", script)

    def test_admin_ui_exposes_full_management_lifecycle(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")

        for label in (
            "系统设置",
            "当前分类",
            "保存配置",
            "删除业务 CPA",
            "清除 OAuth",
            "迁移全部用户",
            "更换管理密钥",
            "设置用户初始密码",
            "审计记录",
            "复制设备码",
            "复制完整输出",
            "CPA 标识",
            "出口代理",
            "保存修改",
        ):
            self.assertIn(label, html + script)
        for endpoint in (
            "/accounts/update",
            "/accounts/reset-quota",
            "/accounts/rebalance",
            "/accounts/clear-auth",
            "/accounts/delete",
            "/users/delete",
            "/settings/management-key",
            "/settings/initial-password",
            "/settings/configuration",
            "/settings/notification-webhook",
            "/settings/notification-webhook/clear",
            "/notifications/send",
        ):
            self.assertIn(endpoint, script)
        for notification_text in (
            "企业微信通知",
            "markdown_v2",
            "保存 Webhook",
            "发送账号信息",
            "近 1 小时用户数",
        ):
            self.assertIn(notification_text, html + script)
        self.assertIn('id="notification-webhook-url" type="url"', script)
        self.assertNotIn("notification-webhook-dialog", html + script)
        self.assertNotIn("notification-card", html)
        self.assertIn("notification-integration", script)
        self.assertIn("notificationWebhookDraft", script)
        self.assertIn("notificationWebhookDirty", script)
        self.assertIn("notifications.webhook_url", script)
        self.assertIn("当前地址已完整显示", script)
        self.assertIn("当前值不会回显", script)
        self.assertIn('group.name === "企业微信通知" ? notificationIntegration()', script)
        self.assertNotIn('id="rebalance-account-users-button"', html)
        self.assertIn('data-account-rebalance="${escapeHTML(account.id)}"', script)
        self.assertIn("const rebalanceAccountUsers = async", script)
        self.assertIn("正在刷新额度并迁移", script)
        self.assertIn("已经开始的请求不会被重放", script)
        self.assertIn('event.target.closest("#save-notification-webhook")', script)
        self.assertIn('event.target.closest("#send-notification-button")', script)
        self.assertIn('api("/notifications/send"', script)
        self.assertIn("notification-webhook-state", script)
        self.assertIn("/admin/app.css?v=20260820-team-period-v1", html)
        self.assertIn("/admin/monitor-utils.js?v=20260812-adaptive-chart-points", html)
        self.assertIn("/admin/view-state-utils.js?v=20260819-view-state-v1", html)
        self.assertIn("/admin/app.js?v=20260820-team-period-weekly-policy-v1", html)
        self.assertLess(
            html.index("/admin/view-state-utils.js"),
            html.index("/admin/app.js"),
        )
        self.assertIn("const renderModelEffortProgress = (model) =>", script)
        self.assertIn("${renderModelEffortProgress(model)}", script)
        self.assertIn("色块表示该模型各推理强度 Token 占比", script)
        self.assertIn('width: min(780px, 100vw)', style)
        self.assertNotIn('id="organization-team-context"', html)
        self.assertNotIn('id="organization-select-all"', html)
        self.assertIn('class="organization-usage-range-readonly"', html)
        self.assertIn('<option value="current_week" selected>本周期</option>', html)
        self.assertIn('id="organization-token-column-label">本周期 Token</th>', html)
        self.assertIn('id="organization-team-relation-header"', html)
        self.assertIn('<option value="current" selected>当前团队成员</option>', html)
        self.assertIn('<option value="all" selected>不限用量</option>', html)
        self.assertIn('organizationUsageWindow: "current_week"', script)
        self.assertIn('openCustomUsageRange("organization")', script)
        self.assertIn('renderOrganizationUsageRange();', script)
        self.assertIn('organizationTeamQuery(state.organizationPage, 50, fresh)', script)
        self.assertIn('const usageQuery = organizationUsageQuery(fresh);', script)
        self.assertNotIn('$("#organization-team-context-name").textContent = item.name;', script)
        self.assertIn('$("#organization-team-relation-header").textContent = `与“${item.name}”的关系`;', script)
        self.assertIn("本团队成员", script)
        self.assertIn("属于其他团队", script)
        self.assertIn("尚未加入", script)
        self.assertIn('data-view="organization"', html)
        self.assertIn('id="view-organization"', html)
        self.assertIn('id="organization-team-status"', html)
        self.assertIn('id="organization-catalog-dialog"', html)
        self.assertIn('id="organization-members-dialog"', html)
        self.assertIn('id="organization-team-assign"', html)
        self.assertNotIn('id="create-team-form"', html)
        self.assertNotIn('id="create-tag-form"', html)
        self.assertIn('event.target.closest("[data-organization-members]")', script)
        self.assertIn('event.target.closest("[data-organization-edit]")', script)
        self.assertIn('event.target.closest("[data-organization-delete]")', script)
        self.assertIn('await saveOrganizationCatalogItem();', script)
        self.assertIn(">团队管理<", html)
        self.assertNotIn(">组织管理<", html)
        self.assertNotIn(">标签管理<", html)
        self.assertNotIn('id="organization-subnav"', html)
        self.assertNotIn('id="organization-tags-panel"', html)
        self.assertNotIn('id="user-tag-filter"', html)
        self.assertNotIn('id="user-classification-tags"', html)
        self.assertNotIn('id="organization-tag-member-table"', html)
        self.assertNotIn('id="organization-create-tag"', html)
        self.assertNotIn('api("/users/tags"', script)
        self.assertNotIn('api("/users/tags/batch"', script)
        self.assertIn('id="team-usage-button"', html)
        self.assertIn("选择团队后查看用量", html)
        self.assertIn('id="team-usage-drawer"', html)
        self.assertNotIn('id="team-usage-panel"', html)
        self.assertIn("team_model_reasoning_effort_tokens", (ROOT / "admin" / "server.py").read_text(encoding="utf-8"))
        self.assertIn("模型与推理强度", script)
        self.assertIn('id="reasoning-effort-colors"', html)
        self.assertIn("configurationEditableFields", script)
        self.assertIn("configurationApplyLabel", script)
        self.assertIn("configurationDirty", script)
        self.assertIn("hydrateConfigurationDraft()", script)
        self.assertIn("configurationValuesFromDraft", script)
        self.assertNotIn("configurationValuesFromForm", script)
        self.assertIn("data-configuration-key", script)
        self.assertIn("selection.isCollapsed", script)
        self.assertIn("Codex device code:", script)
        self.assertIn("该账号已有 OAuth 授权任务，已直接打开", script)
        self.assertIn("window.isSecureContext", script)
        self.assertIn('document.execCommand("copy")', script)
        self.assertIn('document.querySelector("dialog[open]") || document.body', script)
        self.assertIn('state.oauthCode || (visibleCode === "—" ? "" : visibleCode)', script)
        self.assertIn("浏览器拒绝复制，请手动选择文本", script)
        self.assertIn("new_id", script)
        self.assertIn("确认修改", script)
        self.assertIn("revoke_keys: true", script)
        self.assertIn("删除用户", script)
        self.assertNotIn("account-selector", html)
        self.assertNotIn("new-user-account", script)
        self.assertIn('id="new-user-team"', html)
        self.assertIn("团队仅用于用量统计，不影响 CPA 自动分配", html + script)
        self.assertIn('team_id: $("#new-user-team").value || null', script)
        self.assertIn('team.id === state.userTeamFilter', script)
        self.assertIn('id="release-notice"', html)
        self.assertIn('id="release-version"', html)
        self.assertIn('api(`/release${fresh ? "?fresh=1" : ""}`)', script)
        self.assertIn("请在部署环境执行镜像拉取部署", script)
        self.assertIn("RELEASE_POLL_INTERVAL_MS", script)
        self.assertIn("if (payload.keys?.length) showSecrets", script)
        self.assertNotIn("sessionStorage", script)
        self.assertIn('headers["X-CSRF-Token"] = state.csrfToken', script)
        self.assertIn('createSession ? { method: "POST" }', script)
        self.assertIn("后台关联全部已有用户的统一 Key", html)
        self.assertIn('closeDialog("add-account-dialog");\n      showToast(payload.message);', script)
        self.assertNotIn("Token Usage", html)
        self.assertNotIn("sidebar-links", html)
        self.assertIn("界面切换", html)
        for href in ('href="/"', 'href="/usage/"'):
            self.assertIn(href, html)
        self.assertNotIn('href="/native/"', html)
        self.assertNotIn('href="/my-keys/"', html)
        for label in ("返回界面选择", "使用中心", "Key、账号与用量"):
            self.assertIn(label, html)
        self.assertNotIn("CPA 原生界面", html)
        self.assertNotIn("选择业务账号", html)
        self.assertNotIn("CPA 使用中心", html)

    def test_admin_configuration_center_uses_single_group_workspace(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")

        for element_id in (
            "configuration-search-input",
            "configuration-search-results",
            "settings-section-select",
            "configuration-navigation",
            "page-title-detail",
            "page-eyebrow-detail",
            "configuration-impact-summary",
        ):
            self.assertIn(f'id="{element_id}"', html)
        for section in ("configuration", "access", "backups", "storage", "audit"):
            self.assertIn(f'data-settings-panel="{section}"', html)
        self.assertNotIn('class="section-heading configuration-heading"', html)
        self.assertNotIn('<p class="section-kicker">CONFIGURATION CENTER</p><h3>配置中心</h3>', html)
        self.assertNotIn('class="settings-panel-heading"', html)
        self.assertNotIn('id="configuration-group-title"', html)
        self.assertNotIn('<h3>安全归档</h3>', html)
        self.assertIn('class="settings-navigation-fixed"', html)
        self.assertIn('class="settings-navigation-scroll"', html)
        self.assertLess(html.index('settings-category-label'), html.index('class="settings-navigation-scroll"'))
        self.assertGreater(html.index('id="configuration-navigation"'), html.index('class="settings-navigation-scroll"'))
        for contract in (
            "configurationDraft",
            "configurationGroupDirtyCount",
            "configurationSearchMatches",
            "selectConfigurationGroup",
            "renderSettingsSectionSelect",
            'state.configurationDraft[field.key] =',
            'state.configurationDraft = { ...state.configurationOriginal }',
        ):
            self.assertIn(contract, script)
        self.assertIn('data-configuration-result=', script)
        self.assertIn('data-configuration-field=', script)
        self.assertIn("configuration-choice-control", script)
        self.assertIn("data-choice-address", script)
        self.assertIn("当前地址", script)
        self.assertIn('panel.hidden = panel.dataset.settingsPanel !== state.settingsSection', script)
        self.assertIn('$(".settings-workspace-content").scrollTop = 0', script)
        self.assertNotIn('$("#view-settings").scrollTop = 0', script)
        self.assertIn("CONFIGURATION_HEADING_META", script)
        self.assertIn("SETTINGS_SECTION_HEADING_META", script)
        self.assertIn("updatePageHeading", script)
        self.assertIn('backups: ["安全归档", "RECOVERY"]', script)
        self.assertIn(".settings-workspace", style)
        self.assertIn("#view-settings.active { height: 100%;", style)
        self.assertIn("scrollbar-color: transparent transparent;", style)
        self.assertIn("*:hover,\n*:focus-within { scrollbar-color: var(--line-strong) transparent; }", style)
        self.assertIn(".settings-workspace-content { height: 100%;", style)
        self.assertIn("overflow-y: auto; overscroll-behavior: contain;", style)
        self.assertIn("scrollbar-gutter: stable", style)
        self.assertIn(".settings-navigation { position: relative; display: flex;", style)
        self.assertIn(".settings-navigation-fixed { position: relative;", style)
        self.assertIn(".settings-navigation-scroll { min-height: 0; flex: 1 1 auto;", style)
        self.assertIn("overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain;", style)
        self.assertIn("grid-template-rows: auto minmax(0, 1fr)", style)
        self.assertIn(".settings-workspace-content { height: auto; }", style)
        self.assertIn(".configuration-actions { position: sticky", style)
        self.assertIn(".settings-mobile-selector { display: grid", style)
        self.assertIn(".configuration-field-highlight", style)
        self.assertIn(".configuration-choice-control select", style)
        self.assertIn(".configuration-choice-address", style)
        self.assertIn("nullable_integer", script)
        self.assertIn('placeholder="不限额"', script)
        self.assertIn('quota: "下次采集生效"', script)
        self.assertIn("tokenReadableParts", script)
        self.assertIn("tokenInputPresentation", script)
        self.assertIn("updateTokenInputPreview", script)
        self.assertIn("data-token-input-preview", script)
        self.assertIn(".token-input-preview", style)

    def test_admin_overview_scrollbar_is_hidden_until_hover_without_layout_shift(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")

        self.assertIn('/admin/app.css?v=20260820-team-period-v1', html)
        self.assertIn("#view-overview.active {\n  scrollbar-color: transparent transparent;", style)
        self.assertIn("scrollbar-gutter: stable;", style)
        self.assertIn("#view-overview.active:hover,\n#view-overview.active:focus-within", style)
        self.assertIn("#view-overview.active::-webkit-scrollbar { width: 8px; height: 8px; }", style)
        self.assertIn("#view-overview.active::-webkit-scrollbar-thumb {\n  background: transparent;", style)
        self.assertIn("#view-overview.active:hover::-webkit-scrollbar-thumb,", style)
        self.assertNotIn("#view-overview.active::-webkit-scrollbar { width: 0", style)

    def test_admin_overview_replaces_account_cards_with_token_monitor_panels(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        view_state = (ROOT / "admin" / "static" / "view-state-utils.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")

        self.assertNotIn("<h3>CPA 账号状态</h3>", html)
        self.assertNotIn('id="account-grid"', html)
        for element_id in (
            "overview-usage-window",
            "overview-usage-account",
            "overview-usage-user",
            "overview-summary-usage-chart",
            "overview-summary-usage-total",
            "overview-summary-usage-average",
            "overview-summary-usage-maximum",
            "overview-account-usage-chart",
            "overview-account-usage-table",
            "overview-user-usage-chart",
            "overview-user-usage-table",
        ):
            self.assertIn(f'id="{element_id}"', html)
        self.assertIn("所有账号 Token 使用量", html)
        self.assertIn("CPA 账号 Token 使用趋势", html)
        self.assertIn("用户 Token 使用趋势", html)
        self.assertIn("/overview/usage?", script)
        self.assertIn("/overview/catalog", script)
        self.assertIn("AdminViewStateUtils.catalogOptions", script)
        self.assertIn("AdminViewStateUtils.monitorSeriesStatus", script)
        self.assertIn("AdminViewStateUtils.mutationAffectedViews", script)
        self.assertIn("/operations/impact?", script)
        self.assertNotIn("const targetAccount = state.accounts.find", script)
        self.assertNotIn("options: () => state.accounts.map", script)
        self.assertNotIn("options: () => state.users.map", script)
        self.assertIn('return { label: "状态未知", tone: "neutral" };', view_state)
        self.assertIn("选项目录加载失败", script)
        self.assertIn("const renderMonitorChart", script)
        self.assertIn("const renderMonitorTable", script)
        self.assertIn("MonitorUtils.summarizeSeries(", script)
        self.assertIn('variant: "summary"', script)
        self.assertIn("MonitorUtils.sortTooltipSeries(series, index)", script)
        self.assertIn("MonitorUtils.matchesSearchQuery(option.label, search?.value)", script)
        self.assertIn("renderMonitorVariable(kind);", script)
        self.assertIn('overviewUsageSort: {', script)
        self.assertIn('data-monitor-sort="total"', html)
        self.assertIn('data-monitor-table="account"', html)
        self.assertIn('data-monitor-table="user"', html)
        self.assertIn('<option value="today" selected>今日</option>', html)
        self.assertIn('<option value="since_reset">本周期</option>', html)
        self.assertIn('today: "今日"', script)
        self.assertIn('since_reset: "本周期"', script)
        self.assertIn("monitorWindowUsesDateLabels", script)
        self.assertIn("本周期趋势未纳入这些账号", script)
        self.assertEqual(html.count('class="usage-monitor-help"'), 6)
        self.assertIn("最新聚合间隔的 Token 使用值", html)
        self.assertIn("没有请求的间隔按 0 计算", html)
        self.assertIn("单个聚合间隔的最高 Token 使用值", html)
        self.assertNotIn("时间桶", html)
        self.assertIn(".usage-monitor-help:hover::after", style)
        self.assertIn("usage-monitor-crosshair", script)
        self.assertIn('class="usage-monitor-chart-plot"', script)
        self.assertIn('class="usage-monitor-tooltip" data-monitor-tooltip role="tooltip" aria-hidden="true"', script)
        self.assertIn('tooltip.dataset.active = "true"', script)
        self.assertIn('stage.addEventListener("mouseleave", hideTooltip)', script)
        self.assertIn("MonitorUtils.placeTooltip(", script)
        self.assertNotIn("grid-template-columns: minmax(0, 1fr) 240px", style)
        self.assertIn(".usage-monitor-tooltip { position: absolute; z-index: 2;", style)
        self.assertIn("tooltip.style.transform = `translate3d(", script)
        self.assertIn("overviewUsageRequestId", script)
        self.assertIn(".usage-monitor-toolbar", style)
        self.assertIn(".usage-monitor-series", style)
        self.assertIn(".usage-summary-metrics", style)
        self.assertIn(".usage-monitor-summary-area", style)
        self.assertIn("MonitorUtils.adaptivePointIndexes(buckets.length, plotWidth, 10)", script)
        self.assertIn("data-monitor-hover-points", script)
        self.assertIn(".usage-monitor-hover-point", style)
        self.assertNotIn("buckets.length <= 72", script)
        self.assertIn(".usage-monitor-table", style)

    def test_admin_dashboard_supports_saas_theme_and_segmented_overview_range(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")

        self.assertIn('data-theme="light"', html)
        self.assertIn('<meta name="color-scheme" content="light dark">', html)
        self.assertIn('id="theme-toggle"', html)
        self.assertIn('class="usage-time-segments"', html)
        self.assertIn('data-overview-window="custom"', html)
        self.assertIn('data-overview-window="today" aria-pressed="true"', html)
        self.assertIn('data-overview-custom-range-label', html)
        self.assertNotIn('id="overview-custom-range-edit"', html)
        self.assertIn('class="usage-refresh-cluster"', html)
        self.assertIn('@media (max-width: 1620px)', style)
        self.assertIn('const THEME_STORAGE_KEY = "cpa-ui-theme"', script)
        self.assertIn('const LEGACY_THEME_STORAGE_KEY = "cpa-admin-theme"', script)
        self.assertIn('const bootstrapTheme = preferredTheme();', script)
        self.assertIn('applyTheme(document.documentElement.dataset.theme);', script)
        self.assertIn('openCustomUsageRange("overview")', script)
        self.assertIn('overviewUsageWindow: "today"', script)
        self.assertIn('accountUsageWindow: "today"', script)
        self.assertIn(':root[data-theme="dark"]', style)
        self.assertIn('.usage-time-segments button[aria-pressed="true"]', style)
        self.assertIn('.usage-variable { position: relative; display: grid; min-width: 0;', style)
        self.assertIn('.usage-variable-trigger { display: flex; width: 100%; min-width: 0;', style)
        self.assertIn('.usage-variable-select select { width: 100%; min-width: 0; }', style)
        self.assertIn('.usage-refresh-cluster { display: grid; min-width: 0; grid-template-columns: minmax(0, 1fr) auto;', style)
        self.assertIn('--canvas: #f3f6fb;', style)
        self.assertIn('--accent: #6374d8;', style)
        self.assertIn('const enhancePasswordFields =', script)
        self.assertIn('enhancePasswordFields(container);', script)
        self.assertIn('visible ? "隐藏密码" : "显示密码"', script)
        self.assertIn('input.setAttribute("aria-label", fieldLabel);', script)
        self.assertIn('.password-visibility-toggle', style)
        self.assertIn('.password-input > input { width: 100%; padding-right: 46px; }', style)

    def test_service_entry_supports_persistent_light_and_dark_theme(self):
        html = (ROOT / "portal" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "portal" / "landing.js").read_text(encoding="utf-8")
        style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        self.assertIn('<meta name="color-scheme" content="light dark">', html)
        self.assertIn('id="portal-favicon"', html)
        self.assertIn('id="portal-theme-toggle"', html)
        self.assertIn('/portal/landing.js?v=20260815-shared-theme', html)
        self.assertIn('/portal/app.css?v=20260815-theme-password-actions', html)
        self.assertIn('html[data-brand-page="服务入口"][data-theme="dark"]', style)
        self.assertIn('.portal-header-actions', style)
        self.assertIn('.portal-theme-toggle', style)
        self.assertIn('const THEME_STORAGE_KEY = "cpa-ui-theme"', script)
        self.assertIn('window.localStorage.setItem(THEME_STORAGE_KEY, resolved)', script)
        self.assertIn('document.dispatchEvent(new CustomEvent("cpa-theme-change"', script)
        self.assertIn('codex-cpa-cluster-favicon${resolved === "dark" ? "-dark" : ""}.svg', script)

    def test_admin_views_share_a_compact_header_to_release_data_space(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")

        self.assertIn('/admin/app.css?v=20260820-team-period-v1', html)
        self.assertIn('<div class="topbar-heading">', html)
        self.assertLess(html.index('id="page-title"'), html.index('id="page-eyebrow"'))
        self.assertLess(html.index('<h1>进入管理中心</h1>'), html.index('<p class="eyebrow">CONTROL PLANE</p>'))
        self.assertIsNone(re.search(r'<p class="section-kicker"[^>]*>[^<]*</p>\s*<h[1-4]', html))
        self.assertIsNone(re.search(r'<p class="section-kicker"[^>]*>[^<]*</p>\s*<h[1-4]', script))
        self.assertIn(".topbar { position: relative; z-index: 2; display: flex; min-height: 80px;", style)
        self.assertIn(".topbar-heading { display: grid; min-width: 0; gap: 7px; }", style)
        self.assertIn(".topbar .eyebrow { display: flex; min-width: 0;", style)
        self.assertIn(".view { display: none; min-height: 0; padding: 24px 0 72px; }", style)
        self.assertIn(".topbar { min-height: 68px; }", style)
        self.assertIn(".topbar { min-height: 0; align-items: flex-start;", style)
        self.assertIn(".view { padding-top: 14px; padding-bottom: 48px; }", style)
        self.assertNotIn("min-height: 110px", style)
        self.assertNotIn("min-height: 90px", style)
        for view in ("overview", "accounts", "users", "operations", "settings"):
            self.assertIn(f'{view}: [', script)

    def test_admin_account_management_uses_expandable_operational_table(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")
        account_summary = script[
            script.index('return `<tr class="account-summary-row'):
            script.index('<tr class="account-detail-row"')
        ]
        account_detail = script[
            script.index('<tr class="account-detail-row"'):
            script.index('<div class="account-detail-actions">')
        ]

        self.assertIn('/admin/app.css?v=', html)
        self.assertIn('/admin/app.js?v=', html)

        self.assertIn('data-view="accounts"', html)
        self.assertIn('id="view-accounts"', html)
        for column in (
            "CPA 账号",
            "账号状态",
            "OAuth",
            "额度与重置",
            "使用情况",
            "Token",
            "最后使用",
        ):
            self.assertIn(column, html)
        for action in (
            "开始 OAuth",
            "重启容器",
            "查看日志",
            "编辑账号",
            "迁移全部用户",
        ):
            self.assertIn(action, script)
        self.assertNotIn("打开原生界面", script)
        self.assertNotIn("window.location.hostname", script)
        self.assertLess(
            script.index('data-account-edit="${escapeHTML(account.id)}"'),
            script.index('data-account-rebalance="${escapeHTML(account.id)}"'),
        )
        self.assertIn(".account-rebalance-action", style)
        self.assertIn('data-account-row=', script)
        self.assertIn('class="account-detail-row"', script)
        self.assertIn("模型 × 推理强度 Token 明细", script)
        self.assertIn("${renderAccountUsageAnalysis(account)}", account_detail)
        self.assertLess(
            script.index("${renderAccountUsageFacts(account, usage)}"),
            script.index("${renderAccountUsageAnalysis(account)}"),
        )
        self.assertLess(
            script.index("${renderAccountUsageAnalysis(account)}"),
            script.index('<div class="account-detail-actions">'),
        )
        self.assertIn("/accounts/usage-breakdown?", script)
        self.assertIn("groupAccountModelUsage", script)
        self.assertIn("account-model-progress-segment", script)
        self.assertNotIn("account-model-progress-color-", script + style)
        self.assertIn("account-model-effort-${effortColor}", script)
        self.assertIn("var(--account-model-effort-xhigh, #5965c7)", style)
        self.assertIn("var(--account-model-effort-high, #2f73d9)", style)
        self.assertIn('data-tooltip="${escapeHTML(tooltip.join("\\n"))}"', script)
        self.assertIn("const showUserUsageTooltip = (segment) =>", script)
        self.assertIn("user-usage-tooltip-layer", style)
        for tooltip_field in ("调用：", "输入：", "输出：", "推理：", "缓存：", "总 Token："):
            self.assertIn(tooltip_field, script)
        for css_contract in (
            ".account-model-usage",
            ".account-model-usage-row",
            ".account-model-progress",
            ".account-model-progress-segment:hover::after",
            ".account-model-progress-segment:focus::after",
        ):
            self.assertIn(css_contract, style)
        self.assertIn('id="cliproxy-image-manager"', html)
        image_manager = html[
            html.index('<div class="image-manager"'):
            html.index('<div class="notice account-usage-notice"')
        ]
        self.assertNotIn("CPA IMAGE", image_manager)
        self.assertEqual(image_manager.count("更新通道"), 1)
        self.assertLess(
            image_manager.index('id="cliproxy-target-image"'),
            image_manager.index('id="cliproxy-image-summary"'),
        )
        self.assertLess(
            image_manager.index('id="cliproxy-image-summary"'),
            image_manager.index('id="cliproxy-image-state"'),
        )
        self.assertIn('data-operation="image-pull"', html)
        self.assertIn('data-operation="image-update"', html)
        self.assertIn('api("/images/cliproxy")', script)
        self.assertIn('data-operation="image-update"', script)
        self.assertIn("renderImageManager", script)
        self.assertIn('const version = local.version || image.candidate?.version || "版本未知"', script)
        self.assertIn("<span>镜像版本</span>", script)
        self.assertIn("失败时自动恢复原镜像", script)
        self.assertIn("!account.group_enabled || !image.running", script)
        self.assertIn("CPA 账号已停用；启用后再更新镜像", script)
        self.assertIn("停用账号会跳过", script)
        self.assertIn('key === "runtime.cliproxy_image"', script)
        self.assertIn('configurationApplyLabel(field.apply_mode, field.key)', script)
        self.assertIn(".image-manager", style)
        self.assertIn(
            ".accounts-view-active #view-accounts .image-manager",
            style,
        )
        self.assertIn('<span class="table-primary">${escapeHTML(account.id)}</span>', account_summary)
        self.assertNotIn('<span class="table-primary">${escapeHTML(account.group_name)}</span>', account_summary)
        self.assertNotIn('${escapeHTML(account.id)} · :', account_summary)
        self.assertIn("quota.reset_credits?.available_count", script)
        self.assertIn("quota.reset_credits?.credits", script)
        self.assertIn("quota.weekly_windows", script)
        self.assertIn("weekly.reset_at", script)
        self.assertIn('<option value="since_reset">本周期</option>', html)
        self.assertIn('id="account-usage-window" data-enhance-select', html)
        self.assertIn('accountCustomRange: null', script)
        self.assertIn('parse_account_usage_window', (ROOT / "admin" / "server.py").read_text(encoding="utf-8"))
        self.assertIn('state.accountUsageWindow === "since_reset"', script)
        self.assertIn("usage_window_available", script)
        self.assertIn("本周期用量暂不可用", script)
        self.assertIn("下次重置", script)
        self.assertIn("formatFullTime(credit.expires_at)", script)
        self.assertIn('data-quota-reset=', script)
        self.assertIn('class="quota-reset-action"', script)
        self.assertIn('disabled aria-disabled="true"', script)
        self.assertIn("当前没有可用重置额度", script)
        self.assertIn('id="quota-reset-dialog"', html)
        self.assertIn("选择要使用的重置额度", html)
        self.assertIn('id="quota-reset-credit"', html)
        self.assertIn("Full reset 的到期时间", html)
        self.assertIn("操作不可撤销", html)
        self.assertIn('api("/accounts/reset-quota"', script)
        self.assertIn("credit_id: selected.id", script)
        self.assertNotIn("window_key: selected.key", script)
        self.assertIn('colspan="9"', script)
        self.assertIn('api("/accounts/update"', script)
        self.assertIn("group_enabled", script)
        self.assertIn("fallback_account", script)
        self.assertIn('data-account-policy="${escapeHTML(account.id)}"', script)
        self.assertIn('id="account-policy-dialog"', html)
        self.assertNotIn('id="detail-account-enabled"', html)
        self.assertNotIn('id="detail-account-default"', html)
        self.assertNotIn(" · 默认", script)
        detail_dialog = html[
            html.index('<dialog id="account-detail-dialog"'):
            html.index('<dialog id="account-policy-dialog"')
        ]
        self.assertLess(
            detail_dialog.index('class="danger-zone account-danger-zone"'),
            detail_dialog.index("<footer>"),
        )
        self.assertIn("危险操作", detail_dialog)
        self.assertIn("proxy_mode", script)
        self.assertIn('id="new-account-proxy-mode"', html)
        self.assertIn('id="new-account-proxy-url"', html)
        self.assertIn('id="detail-account-proxy-mode"', html)
        self.assertIn('id="detail-account-proxy-url"', html)
        self.assertIn("只重建当前 CPA", html + script)
        self.assertNotIn("账号名称", html)
        self.assertNotIn("new-account-group-name", html)
        self.assertNotIn("detail-account-group-name", html)
        self.assertNotIn("group_name", script)
        for field in (
            "account.email",
            "account.service",
            "account.container_status",
            "account.auth_files",
        ):
            self.assertNotIn(field, account_summary)
            self.assertIn(field, account_detail)
        self.assertNotIn("data-account-manage", script)
        self.assertIn(".account-summary-row", style)
        self.assertIn(".account-detail-facts", style)
        self.assertIn(".quota-reset-cell", style)
        self.assertIn(".quota-reset-action", style)
        self.assertIn(".account-quota-overview", style)
        self.assertIn(".account-activity", style)
        self.assertIn(".account-quota-overview { display: grid; min-height: 46px;", style)
        self.assertIn(".quota-reset-cell { display: grid; min-width: 82px; align-self: stretch; align-content: center;", style)
        self.assertIn(".account-activity { display: grid; min-height: 46px;", style)
        self.assertIn(".account-runtime-facts", style)
        self.assertIn(".account-runtime-facts { grid-template-columns: repeat(6, minmax(0, 1fr)); }", style)
        self.assertIn(".account-usage-facts { grid-template-columns: repeat(7, minmax(0, 1fr)); }", style)
        self.assertIn('<div class="account-token-total-fact"><span>Token 总计</span>${renderTokenUsage(usage.total_tokens)}</div>', script)
        self.assertIn(".account-token-total-fact .token-usage-value", style)
        self.assertIn(".account-token-total-fact { grid-column: 1 / -1; }", style)
        self.assertIn("缓存 Token ÷ 输入 Token", script)
        self.assertIn("formatUsagePercent(usage.cached_tokens, usage.input_tokens)", script)
        self.assertIn('<div class="account-cache-head"><span>缓存 Token</span>', script)
        self.assertIn(".account-cache-head { display: grid;", style)
        self.assertIn("grid-template-columns: minmax(0, 1fr) auto;", style)
        self.assertIn(".account-cache-head > span { overflow: hidden;", style)
        self.assertNotIn(".account-cache-head { display: flex;", style)
        self.assertIn(".account-cache-rate", style)
        self.assertNotIn(".account-cache-rate { position: absolute;", style)
        self.assertIn(".quota-reset-action:disabled", style)
        self.assertEqual(account_summary.count("account-cell-content"), 8)
        self.assertIn(".account-summary-row > td { padding-top: 12px; padding-bottom: 12px; vertical-align: middle; }", style)
        self.assertIn(".account-cell-content { display: flex; min-width: 0; min-height: 54px; align-items: center; }", style)
        self.assertIn(".account-table > thead th:nth-child(8) { width: 9%; }", style)
        self.assertIn('class="number-cell token-total account-token-cell"', account_summary)
        self.assertIn('class="account-cell-content account-token-content"', account_summary)
        self.assertIn(".account-token-content { justify-content: flex-end; text-align: right; }", style)
        self.assertIn(".account-tag-stack { display: flex; align-items: center;", style)
        self.assertIn(".account-runtime-status[data-tooltip]:hover::after", style)
        self.assertIn(".account-runtime-status[data-tooltip]:focus::after", style)
        self.assertIn('data-tooltip="${escapeHTML(runtimeTooltip)}"', account_summary)
        self.assertNotIn("renderRuntimeIssue", script)
        self.assertNotIn("剩余 ${Math.max", script)
        self.assertIn("accountRow && event.target === accountRow", script)
        self.assertIn("account.active_users_1h", script)
        self.assertIn("account.active_user_emails_1h", script)
        self.assertIn("近 1 小时活跃使用者", script)
        self.assertIn(".account-active-users:hover .account-active-users-tooltip", style)
        self.assertIn(".account-active-users:focus .account-active-users-tooltip", style)
        self.assertIn(".account-active-user-email", style)
        self.assertIn("过去滚动 60 分钟内至少发起 1 次业务请求的去重用户", script)
        self.assertIn('<span>路由 <strong>${formatNumber(account.routed_users)}</strong></span>', script)
        self.assertIn('usageUnavailable ? "—" : formatNumber(usage.request_count)', script)
        self.assertIn("grid-template-columns: repeat(3, minmax(0, 1fr))", style)
        self.assertIn(".account-token-content .token-usage-value", style)
        self.assertIn(".account-token-content .token-usage-unit", style)
        self.assertIn('data-quota-reset="${escapeHTML(account.id)}"', script)
        self.assertIn("const updateScrollableView", script)
        self.assertIn("[document.documentElement, document.body]", script)
        self.assertIn('element.classList.toggle("accounts-view-active", accountsActive)', script)
        self.assertIn("updateScrollableView(state.view)", script)
        self.assertIn("updateScrollableView(view)", script)
        self.assertIn("window.scrollTo(0, 0)", script)
        self.assertIn("body.accounts-view-active { position: fixed", style)
        self.assertIn(".accounts-view-active #view-accounts .table-wrap", style)
        self.assertIn("overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain", style)
        self.assertIn(".accounts-view-active #view-accounts .account-table > thead th", style)
        self.assertIn("const accountAvailabilityStatus", script)
        self.assertIn('label: "不可用", tone: "danger"', script)
        self.assertIn('label: "额度未知", tone: "neutral"', script)
        self.assertIn('label: "注意额度", tone: "warning"', script)
        self.assertIn('label: "可用", tone: "success"', script)
        self.assertIn("state.accounts.find((item) => item.id === accountId)", script)
        self.assertIn("accountRuntimeStatus(account).label", script)
        self.assertIn(
            'used >= 100 ? "danger" : used >= 80 ? "warning" : "success"',
            script,
        )
        self.assertIn('<progress class="${tone}" max="100" value="${used}"', script)
        self.assertNotIn("table-progress-reveal", script)
        self.assertNotIn('style="width:${used}%"', script)
        self.assertIn("progress::-webkit-progress-bar { background: #fff", style)
        self.assertIn("progress::-webkit-progress-value { background: var(--accent)", style)
        self.assertIn("*::-webkit-scrollbar { width: 8px; height: 8px; }", style)
        self.assertIn("*::-webkit-scrollbar-thumb {", style)
        self.assertIn("*:hover::-webkit-scrollbar-thumb,", style)
        self.assertIn("*:focus-within::-webkit-scrollbar-thumb {", style)
        self.assertIn(".organization-member-table-wrap::-webkit-scrollbar { display: none; width: 0; height: 0; }", style)

    def test_admin_account_and_user_views_support_custom_usage_ranges(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")
        server = (ROOT / "admin" / "server.py").read_text(encoding="utf-8")

        self.assertEqual(html.count('<option value="custom">自定义…</option>'), 3)
        for element_id in (
            "custom-usage-range-dialog",
            "custom-usage-start-date",
            "custom-usage-start-time",
            "custom-usage-end-date",
            "custom-usage-end-time",
            "custom-usage-range-preview",
            "custom-usage-range-error",
        ):
            self.assertIn(f'id="{element_id}"', html)
        self.assertIn('data-custom-boundary="start"', html)
        self.assertIn('data-custom-boundary="end"', html)
        self.assertIn("包含", html)
        self.assertIn("不包含", html)
        self.assertNotIn('type="datetime-local"', html)
        self.assertEqual(html.count("data-enhance-select"), 8)
        self.assertIn("const enhanceFilterSelects =", script)
        self.assertIn("const renderCustomCalendar =", script)
        self.assertIn(".custom-calendar-days button.selected", style)
        self.assertIn(".management-toolbar { display: grid;", style)
        self.assertIn("const usageRangeQuery =", script)
        self.assertIn('query.set("start_at"', script)
        self.assertIn('query.set("end_at"', script)
        self.assertIn("state.accountCustomRange?.startAt", script)
        self.assertIn("state.userCustomRange?.startAt", script)
        self.assertIn("parse_admin_usage_range", server)
        self.assertIn('CUSTOM_USAGE_WINDOW = "custom"', server)
        self.assertIn(".custom-range-selection", style)
        self.assertIn(".custom-range-boundaries", style)
        self.assertNotIn("border-left: 3px", style)
        self.assertNotIn("box-shadow: inset 3px 0 0", style)

    def test_admin_user_management_uses_expandable_usage_table(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")
        compose = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")
        collector = (ROOT / "admin" / "usage_collector.py").read_text(encoding="utf-8")

        self.assertIn("用户管理", html)
        self.assertNotIn("用户与 Key", html)
        self.assertNotIn("用户与 Key", script)
        for column in ("用户", "状态", "CPA", "使用次数", "Token 用量", "周额度状态", "最后使用"):
            self.assertIn(column, html)
        for column in (
            "CPA 账号",
            "Key 状态",
            "输入 Token",
            "输出 Token",
            "推理 Token",
            "缓存 Token",
            "未加权 Token",
            "加权 Token",
            "实际倍率",
        ):
            self.assertIn(column, script)
        self.assertIn('id="user-usage-window"', html)
        self.assertIn('<option value="today" selected>今日</option>', html)
        self.assertIn('<option value="86400">24 小时</option>', html)
        self.assertIn('<option value="custom">自定义…</option>', html)
        self.assertIn('id="user-usage-window" data-enhance-select', html)
        self.assertIn('userUsageWindow: "today"', script)
        self.assertIn('userCustomRange: null', script)
        self.assertIn('userSort: { field: "tokens", direction: "desc" }', script)
        self.assertIn('data-user-row=', script)
        self.assertIn('class="user-detail-row"', script)
        self.assertIn('data-user-sort="tokens"', html)
        self.assertIn('data-user-sort="quota"', html)
        self.assertIn('view: "summary"', script)
        self.assertIn('sort: state.userSort.field', script)
        self.assertIn('direction: state.userSort.direction', script)
        self.assertIn("const usedTokens = Number(quota.used_tokens);", script)
        user_table_head = html[
            html.index('<table class="user-table">'):html.index("</thead>", html.index('<table class="user-table">'))
        ]
        self.assertIn('class="sort-button active" type="button" data-user-sort="tokens"', user_table_head)
        self.assertIn('class="sort-button" type="button" data-user-sort="email"', user_table_head)
        self.assertIn('class="user-token-column"', user_table_head)
        self.assertIn('class="user-quota-column"', user_table_head)
        self.assertIn('id="user-pagination"', html)
        self.assertIn('id="user-page-size"', html)
        self.assertIn('id="user-page-prev"', html)
        self.assertIn('id="user-page-next"', html)
        self.assertIn('<option value="25">25</option>', html)
        self.assertIn('<option value="50" selected>50</option>', html)
        self.assertIn('<option value="100">100</option>', html)
        self.assertNotIn('<option value="20">20</option>', html)
        self.assertIn("userPageSize: 50", script)
        self.assertIn('element.classList.toggle("users-view-active", usersActive)', script)
        self.assertIn("const users = state.users;", script)
        self.assertIn("state.userPagination = users.pagination", script)
        self.assertIn("currentUserDetail(user.email)", script)
        self.assertIn("loadUserDetail(email)", script)
        self.assertIn("paginationItems(state.userPage, totalPages)", script)
        self.assertIn("state.userPage = 1", script)
        self.assertIn(".table-pagination", style)
        self.assertIn(".pagination-page.active", style)
        self.assertIn("height: 100dvh", style)
        self.assertIn("grid-template-rows: auto minmax(0, 1fr)", style)
        self.assertIn(".view.active { display: block; overflow-x: clip; overflow-y: auto; overscroll-behavior: contain; }", style)
        self.assertIn(".topbar { position: relative; z-index: 2;", style)
        self.assertIn(".users-view-active #view-users .table-wrap", style)
        self.assertIn("overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain", style)
        self.assertIn(".users-view-active #view-users .user-table > thead th", style)
        self.assertNotIn("data-user-email", script)
        self.assertNotIn("user-detail-dialog", html)
        self.assertIn(".user-summary-row", style)
        self.assertIn(".user-account-table", style)
        self.assertIn("模型与推理分析", script)
        self.assertIn("模型 × 推理强度", script)
        self.assertIn("groupAccountModelUsage(breakdownRows)", script)
        self.assertIn('userUsageBreakdownSort: { field: "total_tokens", direction: "desc" }', script)
        self.assertIn('userAccountSort: { field: "total_tokens", direction: "desc" }', script)
        self.assertIn('attribute: "data-user-breakdown-sort"', script)
        self.assertIn('attribute: "data-user-account-sort"', script)
        self.assertIn('event.target.closest("[data-user-breakdown-sort]")', script)
        self.assertIn('event.target.closest("[data-user-account-sort]")', script)
        self.assertNotIn("drawUsageDonuts", script)
        self.assertNotIn("data-usage-donut", script)
        self.assertIn("usage-model-table", script)
        self.assertIn("usage-model-token-details", script)
        self.assertIn("userUsageTooltip", script)
        self.assertIn(
            ".usage-analysis-summary > div { display: grid; min-width: 0; "
            "grid-template-columns: repeat(2, minmax(0, 1fr)); "
            "align-items: center;",
            style,
        )
        self.assertIn(
            ".usage-analysis-summary > div > span { justify-self: center; "
            "text-align: center; }",
            style,
        )
        self.assertIn(
            ".usage-analysis-summary .usage-analysis-token-stat strong "
            "{ justify-self: end; text-align: right; }",
            style,
        )
        self.assertIn("`加权 Token：${formatNumber(effort.weighted_tokens ?? effort.total_tokens)}`", script)
        self.assertIn('class="account-model-progress-segment account-model-effort-${effortColor}', script)
        self.assertIn('data-tooltip="${escapeHTML(tooltip.join("\\n"))}"', script)
        self.assertIn("<div><dt>输入</dt><dd>${renderTokenUsage(inputTokens)}</dd></div>", script)
        self.assertIn("<div><dt>输出</dt><dd>${renderTokenUsage(outputTokens)}</dd></div>", script)
        self.assertIn("<div><dt>推理</dt><dd>${renderTokenUsage(reasoningTokens)}</dd></div>", script)
        self.assertIn("<div><dt>缓存</dt><dd>${renderTokenUsage(cachedTokens)}</dd></div>", script)
        self.assertNotIn("推理强度占比", script)
        self.assertNotIn("按成功模型调用次数计算占比", script)
        self.assertIn("Math.round(totalTokens / successCount)", script)
        self.assertIn("${renderTokenUsage(item.total_tokens)}", script)
        self.assertIn('field: "total_tokens", label: "未加权 Token"', script)
        self.assertIn('field: "weighted_tokens", label: "加权 Token"', script)
        self.assertIn('field: "multiplier", label: "实际倍率"', script)
        self.assertIn('field: "average_total", label: "平均/次"', script)
        self.assertNotIn("<th>推理 Token</th><th>平均/次</th>", script)
        self.assertIn("/users/usage-breakdown?", script)
        self.assertIn("data-user-usage-account", script)
        self.assertIn('id="user-quota-dialog"', html)
        self.assertIn('class="dialog user-quota-drawer"', html)
        self.assertIn('<dl class="user-quota-summary" id="user-quota-summary"></dl>', html)
        self.assertIn(".user-quota-drawer[open]", style)
        self.assertIn(".user-token-cell .token-usage { align-items: flex-end; }", style)
        self.assertIn("const userUsageWindowLabel = () =>", script)
        self.assertIn("const renderUserTokenCell = (user) =>", script)
        self.assertIn("quota.raw_used_tokens", script)
        self.assertNotIn(">本周实际</span>", script)
        self.assertIn(">本周加权用量</span>", script)
        self.assertIn(">本周未加权</span>", script)
        self.assertIn("本周已重置", script)
        self.assertNotIn('class="user-token-stat user-token-week"', script)
        self.assertIn("${renderTokenUsage(weightedUsed)}", script)
        self.assertIn(".user-quota-primary", style)
        self.assertIn(".user-quota-meter-copy", style)
        self.assertIn(".user-quota-meter-copy .token-usage", style)
        self.assertIn(".user-quota-progress-copy", style)
        self.assertIn('<progress class="user-quota-progress"', script)
        self.assertIn('value="${progressValue}" max="100"', script)
        self.assertIn(".user-quota-progress::-webkit-progress-bar { background: #fff; }", style)
        self.assertIn(".user-quota-progress::-webkit-progress-value", style)
        self.assertIn("background: #fff; border: 1px solid var(--line-strong)", style)
        self.assertIn('aria-label="本周额度使用比例"', script)
        self.assertNotIn('class="user-quota-used"', script)
        self.assertIn("<dt>本周加权已用</dt><dd>", script)
        self.assertIn("<dt>本周未加权</dt><dd>", script)
        self.assertIn("<dt>当前加权上限</dt><dd>", script)
        self.assertIn('data-user-quota=', script)
        self.assertIn('method: "PUT"', script)
        self.assertIn('api("/users/quota"', script)
        account_management_renderer = script[
            script.index("const renderAccounts = () =>"):
            script.index("const renderUserUsageAnalysis = (user) =>")
        ]
        self.assertNotIn("weighted_tokens", account_management_renderer)
        self.assertNotIn("加权 Token", account_management_renderer)
        self.assertIn("全部 CPA 的 Token 总量汇总", html)
        self.assertIn("已经开始的请求（含流式输出）可以完成", html)
        self.assertIn("ensure_usage_breakdown_started", collector)
        self.assertIn(".user-usage-analysis", style)
        self.assertNotIn(".usage-donut", style)
        self.assertNotIn(".usage-donut-canvas", style)
        self.assertNotIn(".usage-combination-legend", style)
        self.assertIn(".usage-model-table", style)
        self.assertIn(".usage-model-token-details", style)
        self.assertNotIn(".usage-model-token-details .token-usage-exact { display: none; }", style)
        self.assertIn(".usage-breakdown-table", style)
        self.assertIn("usage-collector:", compose)
        self.assertIn("log-maintenance:", compose)
        self.assertIn('max-size: "20m"', compose)
        self.assertIn('max-file: "3"', compose)
        self.assertIn("account-runtime-status", script)
        self.assertIn("runtimeDetail(account.runtime, runtime)", script)
        self.assertIn("account.operational_status", script)
        self.assertIn("rate_429_count", script)
        self.assertIn("限流中", script)
        self.assertIn('command("LPOP", "usage", self.batch_size)', collector)

    def test_user_quota_system_settings_are_a_dedicated_group(self):
        control = (ROOT / "scripts" / "cliproxy.py").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")

        quota_start = control.index('"key": "user_quota.default_weekly_tokens"')
        quota_end = control.index('"key": "notification.enabled"')
        quota_settings = control[quota_start:quota_end]
        self.assertIn('"group": "用户额度"', quota_settings)
        self.assertIn('"label": "用户周额度系统默认值"', quota_settings)
        self.assertIn('"type": "nullable_integer"', quota_settings)
        self.assertIn(
            '"key": "user_quota.reset_personal_weekly_on_new_week"',
            quota_settings,
        )
        self.assertIn('"label": "新周恢复默认个人额度"', quota_settings)
        self.assertIn('"default": True', quota_settings)
        self.assertIn('"key": "user_quota.fail_open_after_seconds"', quota_settings)
        self.assertIn(
            '"用户额度": "全部用户的系统默认周额度、个人策略自动恢复与网关故障策略"',
            script,
        )
        self.assertIn('"group": "推理强度策略"', control)
        self.assertIn('("max", "Max", 2.0)', control)
        self.assertIn(
            '"推理强度策略": "同一处管理用户额度倍率和账号 Token 明细配色；两类配置独立生效"',
            script,
        )
        self.assertIn('"type": "color"', control)
        self.assertIn('admin.account_usage.reasoning_effort_color.{}', control)
        self.assertIn('("xhigh", "XHigh", "#5965c7")', control)
        self.assertIn("reasoningEffortStrategyEditor", script)
        self.assertIn('data-reasoning-strategy-reset="multiplier"', script)
        self.assertIn('data-reasoning-strategy-reset="color"', script)
        self.assertIn("reasoning-color-preview-canvas", script)
        self.assertIn("refreshReasoningEffortColorStylesheet", script)
        self.assertIn(".reasoning-strategy-table", style)
        self.assertIn(".reasoning-color-inputs", style)

    def test_account_failover_settings_are_rendered_as_a_live_configuration_group(self):
        control = (ROOT / "scripts" / "cliproxy.py").read_text(encoding="utf-8")
        script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")

        self.assertIn('"key": "account_failover.mode"', control)
        self.assertNotIn('"value": "observe", "label": "观察"', control)
        self.assertIn('"value": "active", "label": "自动执行"', control)
        self.assertIn('"key": "account_failover.reserve_percent"', control)
        self.assertIn('"group": "账号自动切换"', control)
        self.assertIn(
            '"账号自动切换": "官方账号额度耗尽后按剩余资源批量迁移用户路由"',
            script,
        )

    def test_user_quota_operations_have_single_bulk_and_protected_all_user_flows(self):
        html = (ROOT / "admin" / "static" / "index.html").read_text(
            encoding="utf-8"
        )
        script = (ROOT / "admin" / "static" / "app.js").read_text(
            encoding="utf-8"
        )
        view_state = (ROOT / "admin" / "static" / "view-state-utils.js").read_text(
            encoding="utf-8"
        )
        style = (ROOT / "admin" / "static" / "app.css").read_text(
            encoding="utf-8"
        )

        for marker in (
            'id="user-selection-bar"',
            'id="user-select-page"',
            'id="user-bulk-restore-default"',
            'id="user-bulk-reset-usage"',
            'id="user-quota-add-bonus"',
            'id="user-quota-restore-default"',
            'id="user-quota-reset-usage"',
            'id="user-quota-action-dialog"',
            'id="user-quota-action-reason"',
            'id="user-quota-action-confirm"',
            'id="user-quota-unlimited-copy"',
            'id="user-quota-custom-copy"',
        ):
            self.assertIn(marker, html)
        self.assertIn('data-user-select="${escapeHTML(user.email)}"', script)
        self.assertIn('api("/users/quota-actions"', script)
        self.assertIn('"reset_all_current_week_usage"', script)
        self.assertIn('"确认清零全部"', script)
        self.assertIn("quotaSystemDangerZone", script)
        self.assertIn("仅本周生效，下周恢复组织默认", script)
        self.assertIn("持续生效，直到手动恢复组织默认", script)
        self.assertIn('id="quota-reset-all-users"', script)
        self.assertIn("AdminViewStateUtils.allUserQuotaImpact(summary)", script)
        self.assertIn('users: scope === "all" ? [] : targetUsers.map', script)
        self.assertNotIn('scope === "all"\n      ? state.users', script)
        self.assertIn("影响范围暂不可确认", script + view_state)
        self.assertIn("原始 Token 事件与统计历史保持不变", script)
        self.assertIn(".user-selection-bar", style)
        self.assertIn(".user-quota-operations", style)
        self.assertIn(".quota-action-impact", style)
        self.assertIn(".quota-system-danger", style)
        self.assertIn(':root[data-theme="dark"] .quota-system-danger', style)
        self.assertIn(
            "background: color-mix(in srgb, var(--danger-soft) 62%, var(--surface))",
            style,
        )

    def test_usage_dashboard_merges_group_account_and_personal_data(self):
        html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")
        style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        table_head = html[html.index('<table class="usage-account-table">'):html.index("</thead>")]
        summary_row = script[
            script.index('return `<tr class="usage-summary-row'):script.index("</tr>${detailRow(group)}")
        ]
        self.assertEqual(table_head.count('scope="col"'), 10)
        self.assertEqual(summary_row.count("<td"), 10)
        self.assertIn('colspan="10"', script)
        self.assertNotIn('scope="colgroup"', html)
        self.assertIn('id="request-window-label"', html)
        self.assertIn('id="token-window-label"', html)
        self.assertIn('class="usage-token-header"', table_head)
        self.assertIn('data-window="3600"', html)
        self.assertIn('data-window="today" aria-pressed="true"', html)
        self.assertIn('data-window="86400" aria-pressed="false"', html)
        self.assertIn('data-window="604800"', html)
        self.assertIn('id="request-window-label"', html)
        self.assertIn('id="token-window-label"', html)
        self.assertIn('windowSeconds: "today"', script)
        self.assertIn("grid-template-columns: repeat(4, 1fr)", style)
        self.assertIn("active_users_1h", script)
        self.assertIn("usage.request_count", script)
        self.assertIn("usage.total_tokens", script)
        self.assertIn('class="usage-token-cell"', summary_row)
        self.assertIn('class="usage-token-content"', summary_row)
        self.assertIn(".usage-account-table {\n  width: 100%;\n  min-width: 0;", style)
        self.assertIn(".usage-account-table thead th {", style)
        self.assertIn("  text-align: center;", style)
        self.assertIn(".usage-account-table .usage-token-header { text-align: center; }", style)
        self.assertIn(".usage-account-table .usage-token-cell { text-align: right; }", style)
        centered_cells = style[
            style.index(".usage-account-table td:nth-child(1),"):
            style.index(".table-index-column,\n.table-index-cell { font-family")
        ]
        for column in (1, 2, 5, 6, 7, 9, 10):
            self.assertIn(".usage-account-table td:nth-child({})".format(column), centered_cells)
        self.assertIn(".usage-cell-number { display: block; text-align: inherit;", style)
        self.assertIn(".usage-token-content { display: flex; min-height: 50px; align-items: center; justify-content: flex-end; }", style)
        self.assertIn("usage.input_tokens", script)
        self.assertIn("usage.output_tokens", script)
        self.assertIn("usage.total_tokens", script)
        self.assertIn("usage.weighted_tokens", script)
        self.assertIn("未加权 Token", script)
        self.assertIn("加权 Token", script)
        self.assertIn("userTokenPair(usage)", summary_row)
        self.assertIn(".usage-user-token-pair", style)

        self.assertIn("usageWindowLabel", script)
        self.assertIn('today: "今日"', script)
        self.assertIn("state.windowSeconds = button.dataset.window", script)
        self.assertIn("weekly.used_percent", script)
        self.assertIn("weekly.limit_reached", script)
        self.assertIn("payload.quota_generated_at", script)
        self.assertIn("payload.quota_cached", script)
        self.assertIn('<strong class="usage-account-id">${escapeHTML(group.account)}</strong>', summary_row)
        self.assertNotIn("group.name", script)
        self.assertNotIn("分组与账号", html)
        self.assertIn('<progress class="usage-quota-track"', script)
        self.assertNotIn("table-progress-reveal", script)
        self.assertIn("*::-webkit-scrollbar { width: 8px; height: 8px; }", style)
        self.assertIn("*::-webkit-scrollbar-thumb {", style)
        self.assertIn("*:hover::-webkit-scrollbar-thumb,", style)
        self.assertNotIn("scrollbar-width: none", style)
        self.assertNotIn("width: 0; height: 0", style)
        self.assertNotIn('style="width:', script)
        self.assertIn("group.current", script)
        self.assertIn("确认切换", html)
        for element_id in (
            "current-account-name",
            "current-account-status",
            "current-account-quota-copy",
            "current-account-quota-remaining",
            "current-account-quota-track",
        ):
            self.assertIn('id="{}"'.format(element_id), html)
        self.assertIn("const renderCurrentAccount = (payload) =>", script)
        self.assertIn("groups.find((group) => group.current)", script)
        self.assertIn("renderCurrentAccount(payload);", script)
        self.assertIn(".usage-detail-heading", style)
        self.assertIn(".usage-token-grid", style)
        self.assertIn("grid-template-columns: minmax(170px, .85fr) minmax(0, 5.15fr);", style)
        self.assertIn("@media (max-width: 680px)", style)

    def test_table_layout_uses_semantic_alignment_and_bounded_widths(self):
        admin_html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        admin_style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")
        portal_style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        self.assertIn("thead th { text-align: center; }", admin_style)
        self.assertIn(".usage-monitor-table td:first-child { text-align: left; }", admin_style)
        self.assertIn(".usage-monitor-table td:nth-child(2) { text-align: center; }", admin_style)
        self.assertIn(".usage-monitor-table td:not(:first-child):not(:nth-child(2)) { text-align: right; }", admin_style)
        self.assertIn(".account-table { width: 100%; min-width: 0; table-layout: fixed; }", admin_style)
        self.assertIn(".user-table { width: 100%; min-width: 0; table-layout: fixed; }", admin_style)
        self.assertIn(".organization-catalog-table { width: 100%; min-width: 0; table-layout: fixed; }", admin_style)
        self.assertIn(".organization-member-table td:nth-child(2) { padding-inline: 0; text-align: center; }", admin_style)
        self.assertIn(".user-table .user-select-cell { padding-inline: 0; }", admin_style)
        self.assertIn(".user-summary-row > td:nth-child(7) { white-space: normal; }", admin_style)
        self.assertIn("#organization-team-member-table .organization-member-table td:nth-child(4)", admin_style)
        self.assertNotIn("#organization-tag-member-table", admin_style)
        self.assertIn("#organization-catalog-dialog .dialog-body { display: grid; gap: 18px; }", admin_style)
        self.assertIn("button:not(.active):hover { color: var(--ink); background: color-mix(in srgb, var(--ink) 5%, transparent); }", admin_style)
        self.assertNotIn(".scrollbar-edge-active::-webkit-scrollbar-thumb", admin_style)
        self.assertNotIn("const TABLE_SCROLL_REGION_SELECTOR", (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8"))
        self.assertIn(".organization-members-body { display: flex; min-height: 0; flex-direction: column; overflow: hidden;", admin_style)
        self.assertIn(".organization-member-table-wrap { min-width: 0; min-height: 0; flex: 1 1 auto; overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; border-bottom: 1px solid var(--line); scrollbar-width: none; }", admin_style)
        self.assertIn(".organization-member-table-wrap::-webkit-scrollbar { display: none; width: 0; height: 0; }", admin_style)
        self.assertIn(".team-detail-summary { display: grid; overflow: hidden; grid-template-columns: minmax(0, 1fr);", admin_style)
        self.assertIn(".team-detail-facts > div { display: grid; min-width: 0;", admin_style)
        self.assertIn(".team-detail-facts .token-usage-exact { max-width: 100%; overflow-wrap: anywhere;", admin_style)
        self.assertIn(".team-combination-progress .account-model-progress { height: 30px; }", admin_style)
        self.assertIn("body:has(.dialog[open]) { overflow: hidden !important; }", admin_style)
        self.assertIn(".dialog { max-width: none; max-height: none; overflow: hidden;", admin_style)
        self.assertIn(".dialog-card::-webkit-scrollbar { display: none; width: 0; height: 0; }", admin_style)
        self.assertNotIn('const TABLE_SCROLL_REGION_SELECTOR', (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8"))
        self.assertNotIn('id="organization-members-subtitle"', admin_html)
        self.assertNotIn('id="organization-catalog-dialog-subtitle"', admin_html)
        self.assertNotIn("团队调整不会重写历史 Token；标签不参与团队统计。", admin_html)
        self.assertNotIn("按事件发生时团队归属统计，调整团队不会重写历史。", admin_html)
        self.assertIn("团队报表会按当前成员动态汇总所选范围内的 Token", admin_html)
        self.assertIn("报表按当前成员动态汇总所选范围内的 Token", (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8"))
        self.assertIn(".service-table { width: 100%; min-width: 0; table-layout: fixed; }", admin_style)
        self.assertIn(".usage-model-table { width: 100%; min-width: 0; table-layout: fixed; }", admin_style)
        self.assertIn(".usage-breakdown-table { width: 100%; min-width: 0; table-layout: fixed; }", admin_style)
        self.assertIn(".user-account-table { width: 100%; min-width: 0; table-layout: fixed; }", admin_style)
        self.assertIn(".number-cell { text-align: right;", admin_style)
        self.assertIn(".table-index-cell { width: 50px; text-align: center;", admin_style)
        self.assertIn('class="service-table"', admin_html)
        self.assertIn('class="storage-table"', admin_html)
        self.assertIn('class="audit-table"', admin_html)
        self.assertIn(".settings-table-panel th:nth-child(1) { width: 8%; }", admin_style)
        self.assertIn(".storage-table th:nth-child(3) { width: 38%; }", admin_style)
        self.assertIn(".pagination-pages { max-width: 100%; overflow-x: hidden; flex-wrap: wrap; }", admin_style)
        self.assertIn(".usage-time-segments { overflow-x: hidden; flex-wrap: wrap; }", admin_style)

        self.assertIn(".self-key-table thead th { text-align: center; }", portal_style)
        self.assertIn(".self-key-table td:nth-child(5) { text-align: right; }", portal_style)
        self.assertIn(".usage-account-table thead th {", portal_style)
        centered_cells = portal_style[
            portal_style.index(".usage-account-table td:nth-child(1),"):
            portal_style.index(".table-index-column,\n.table-index-cell { font-family")
        ]
        for column in (1, 2, 5, 6, 7, 9, 10):
            self.assertIn(".usage-account-table td:nth-child({})".format(column), centered_cells)
        self.assertIn(".usage-account-table td:nth-child(8) { text-align: right; }", portal_style)

    def test_admin_token_usage_uses_shared_unit_formatter(self):
        admin_html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        admin_script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        admin_style = (ROOT / "admin" / "static" / "app.css").read_text(encoding="utf-8")

        self.assertIn("/portal/token-usage.js", admin_html)
        self.assertLess(
            admin_html.index("/portal/token-usage.js"),
            admin_html.index("/admin/app.js"),
        )
        self.assertIn('/admin/app.css?v=20260820-team-period-v1', admin_html)
        self.assertIn('/admin/monitor-utils.js?v=20260812-adaptive-chart-points', admin_html)
        self.assertIn('/admin/view-state-utils.js?v=20260819-view-state-v1', admin_html)
        self.assertIn('/admin/app.js?v=20260820-team-period-weekly-policy-v1', admin_html)
        self.assertIn('if (showFeedback) accountQuery.set("fresh", "1");', admin_script)
        self.assertIn("accounts.quota_generated_at", admin_script)
        self.assertIn("accounts.quota_cached", admin_script)
        self.assertIn("accounts.quota_refreshing", admin_script)
        self.assertIn("weekly.limit_reached || quota.limit_reached", admin_script)
        self.assertIn(
            "const renderTokenUsage = (value) => TokenUsageFormatter.render(value);",
            admin_script,
        )
        token_expressions = (
            "usage.total_tokens",
            "usage.input_tokens",
            "usage.output_tokens",
            "usage.reasoning_tokens",
            "usage.cached_tokens",
            "accountUsage.total_tokens",
            "accountUsage.input_tokens",
            "accountUsage.output_tokens",
            "accountUsage.reasoning_tokens",
            "accountUsage.cached_tokens",
        )
        for expression in token_expressions:
            self.assertIn(f"renderTokenUsage({expression})", admin_script)
            self.assertNotIn(f"formatNumber({expression})", admin_script)
        self.assertNotIn("formatCompactNumber", admin_script)
        self.assertIn(".token-usage-unit", admin_style)
        self.assertIn(".token-usage-exact", admin_style)
        self.assertIn(".token-usage-sr-only", admin_style)

    def test_admin_periodic_refresh_does_not_duplicate_overview_usage_query(self):
        admin_script = (ROOT / "admin" / "static" / "app.js").read_text(
            encoding="utf-8"
        )

        self.assertIn(
            'if (!showFeedback && !state.overviewUsage) loadOverviewUsage(false);',
            admin_script,
        )
        self.assertIn(
            'if (view === "overview") scheduleOverviewUsageRefresh();',
            admin_script,
        )
        self.assertIn("const scheduleViewRefresh =", admin_script)
        self.assertIn("if (!state.authenticated || document.hidden) return;", admin_script)
        self.assertIn('state.view !== "overview"', admin_script)
        self.assertIn('if (fresh) query.set("fresh", "1");', admin_script)
        self.assertIn('const usagePath = overviewUsagePath(fresh);', admin_script)
        self.assertIn(
            'await Promise.all([overviewRequest, loadOverviewUsage(true, false)])',
            admin_script,
        )
        self.assertIn('if (fresh) query.set("fresh", "1");', admin_script)
        self.assertIn('api(userSummaryPath(showFeedback))', admin_script)
        self.assertIn(
            'api(`/overview${showFeedback ? "?fresh=1" : ""}`)',
            admin_script,
        )

    def test_usage_center_exposes_codex_history_recovery_agent_prompt(self):
        html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")
        style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        for value in (
            'id="codex-history-card"',
            "切换 API Key 后，Codex 旧会话不见了？",
            'id="open-codex-history"',
            "让 Codex 完整处理",
            "旧会话恢复",
            'id="codex-history-dialog"',
            'id="codex-history-agent-prompt"',
            'id="copy-codex-history"',
            "复制 Agent 指令",
        ):
            self.assertIn(value, html)

        prompt = script[
            script.index("const buildCodexHistoryAgentPrompt"):
            script.index("const weeklyCell")
        ]
        self.assertIn("由于登录方式已从 OAuth 变为 API Key", prompt)
        self.assertIn("将 Codex 之前的会话迁移到当前 API Key 的会话历史中", prompt)
        self.assertNotIn("state.payload?.api_key", prompt)
        self.assertIn('$("#codex-history-card").hidden = false', script)
        self.assertIn('$("#codex-history-card").hidden = true', script)
        self.assertIn('$("#codex-history-dialog").showModal()', script)
        self.assertIn("copyText(buildCodexHistoryAgentPrompt())", script)
        self.assertIn(".usage-history-help", style)
        self.assertIn(".usage-agent-dialog", style)
        self.assertIn(".usage-agent-prompt", style)

    def test_usage_center_token_usage_uses_shared_unit_formatter(self):
        dashboard_html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        portal_script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")
        portal_style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        self.assertIn("/portal/token-usage.js", dashboard_html)
        self.assertLess(
            dashboard_html.index("/portal/token-usage.js"),
            dashboard_html.index("/portal/my-keys.js"),
        )
        self.assertIn('/portal/app.css?v=20260818-password-visibility-v1', dashboard_html)
        self.assertIn('/portal/my-keys.js?v=20260818-password-visibility-v1', dashboard_html)
        self.assertIn('data-theme="light"', dashboard_html)
        self.assertIn('id="usage-favicon"', dashboard_html)
        self.assertIn('id="usage-theme-toggle"', dashboard_html)
        self.assertIn('html[data-brand-page="使用中心"][data-theme="dark"]', portal_style)
        self.assertIn('document.documentElement.dataset.theme = resolved;', portal_script)
        self.assertIn('codex-cpa-cluster-favicon${resolved === "dark" ? "-dark" : ""}.svg', portal_script)
        self.assertIn('const THEME_STORAGE_KEY = "cpa-ui-theme"', portal_script)
        self.assertIn('window.localStorage.setItem(THEME_STORAGE_KEY, resolved)', portal_script)
        self.assertNotIn("border-left: 3px", portal_style)
        self.assertNotIn("box-shadow: inset 3px 0 0", portal_style)
        self.assertIn('fetch("/usage/me/route", { cache: "no-store" })', portal_script)
        self.assertIn('window.setInterval(checkCurrentRoute, 5_000);', portal_script)
        self.assertIn('document.addEventListener("visibilitychange"', portal_script)
        self.assertIn('window.addEventListener("pageshow", checkCurrentRoute);', portal_script)
        self.assertIn('window.addEventListener("focus", checkCurrentRoute);', portal_script)
        self.assertIn(
            "const renderTokenUsage = (value) => TokenUsageFormatter.render(value);",
            portal_script,
        )
        token_expressions = (
            "usage.total_tokens",
            "usage.input_tokens",
            "usage.output_tokens",
            "usage.reasoning_tokens",
            "usage.cached_tokens",
        )
        for expression in token_expressions:
            self.assertIn(f"renderTokenUsage({expression})", portal_script)
            self.assertNotIn(f"formatNumber({expression})", portal_script)
        self.assertIn("缓存 Token ÷ 输入 Token", portal_script)
        self.assertIn("formatUsagePercent(usage.cached_tokens, usage.input_tokens)", portal_script)
        self.assertIn('if (denominator <= 0) return "0%";', portal_script)
        self.assertIn('<div class="usage-cache-head"><span>缓存 Token</span>', portal_script)
        self.assertIn(".usage-cache-head { display: grid;", portal_style)
        self.assertIn(".usage-cache-head > span { overflow: hidden;", portal_style)
        self.assertIn(".usage-cache-rate { min-width: max-content;", portal_style)
        self.assertNotIn(".usage-cache-rate { position: absolute;", portal_style)
        self.assertIn(".account-model-usage { display: grid;", portal_style)
        self.assertIn(".account-model-progress-segment", portal_style)
        self.assertIn("var(--account-model-effort-xhigh, #5965c7)", portal_style)
        self.assertIn(".usage-account-detail", portal_style)
        self.assertIn(".token-usage-unit", portal_style)
        self.assertIn(".token-usage-exact", portal_style)
        self.assertIn(".token-usage-sr-only", portal_style)

    def test_every_business_table_has_a_sequential_index_column(self):
        dashboard_html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        portal_script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")
        admin_html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        admin_script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")

        for source, expected_tables in (
            (dashboard_html, 1),
            (admin_html, 7),
            (admin_script, 3),
        ):
            table_starts = []
            offset = 0
            while True:
                start = source.find("<table", offset)
                if start < 0:
                    break
                offset = start + len("<table")
                open_tag_end = source.index(">", start)
                open_tag = source[start:open_tag_end]
                if 'class="usage-monitor-table"' in open_tag:
                    continue
                table_starts.append(start)
            self.assertEqual(len(table_starts), expected_tables)
            for start in table_starts:
                header_end = source.index("</thead>", start)
                header = source[start:header_end]
                self.assertIn('class="table-index-column"', header)
                self.assertIn("序号</th>", header)

        self.assertEqual(admin_html.count('class="usage-monitor-table"'), 2)
        self.assertEqual(admin_script.count('<td class="table-index-cell">'), 10)
        self.assertEqual(portal_script.count('class="table-index-cell"'), 1)
        self.assertIn('${startIndex + index + 1}</td>', admin_script)
        self.assertIn('${accountIndex + 1}</td>', admin_script)
        self.assertIn('data-label="序号">${index + 1}</td>', portal_script)

    def test_account_tables_support_quota_first_sorting(self):
        admin_html = (ROOT / "admin" / "static" / "index.html").read_text(encoding="utf-8")
        admin_script = (ROOT / "admin" / "static" / "app.js").read_text(encoding="utf-8")
        dashboard_html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        portal_script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")
        portal_style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        self.assertIn('accountSort: { field: "quota", direction: "asc" }', admin_script)
        self.assertIn('data-account-sort="quota" data-direction="asc"', admin_html)
        for field in ("account", "runtime", "auth", "quota", "activity", "tokens", "last_used"):
            self.assertIn('data-account-sort="{}"'.format(field), admin_html)
        self.assertIn("compareTableValues(accountSortValue(left), accountSortValue(right)", admin_script)
        self.assertIn("const accountQuotaSortValue", admin_script)
        self.assertIn('event.target.closest("[data-account-sort]")', admin_script)
        self.assertIn("const serviceStateRank", admin_script)

        self.assertIn('sort: { field: "quota", direction: "asc", pinCurrent: true }', portal_script)
        self.assertIn('data-usage-sort="quota" data-direction="asc"', dashboard_html)
        self.assertIn("state.sort.pinCurrent && left.current !== right.current", portal_script)
        self.assertIn("const quotaSortValue", portal_script)
        self.assertIn("当前账号固定在第一行", dashboard_html)
        for field in ("current", "account", "quota", "active_users", "status", "requests", "tokens", "last_used"):
            self.assertIn('data-usage-sort="{}"'.format(field), dashboard_html)
        self.assertIn("compareTableValues(sortValue(left), sortValue(right)", portal_script)
        self.assertIn(".usage-sort-button.active::after", portal_style)

    def test_usage_center_reports_automatic_route_assignment(self):
        dashboard_html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        portal_script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")
        portal_style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        self.assertIn('id="route-notice"', dashboard_html)
        self.assertIn("暂时无法自动分配 CPA", dashboard_html)
        self.assertIn("系统会在下次刷新时自动重试", dashboard_html)
        self.assertIn('routeNotice.hidden = Boolean(payload.current_group);', portal_script)
        self.assertIn('payload.route_assignment?.status === "assigned"', portal_script)
        self.assertIn("已自动分配至", portal_script)
        self.assertIn('hasCurrentGroup ? "切换" : "选择"', portal_script)
        self.assertIn("选择后 API Key 将立即生效", portal_script)
        self.assertIn(".usage-route-notice", portal_style)

    def test_usage_center_keeps_controls_fixed_and_only_scrolls_the_table(self):
        dashboard_html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        portal_style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        header_end = dashboard_html.index("</header>")
        content_start = dashboard_html.index('class="usage-center-content"')
        key_card = dashboard_html.index('id="key-card"')
        self.assertLess(header_end, content_start)
        self.assertLess(content_start, key_card)
        self.assertIn(".usage-center-body {\n  height: 100vh;\n  height: 100dvh;\n  overflow: hidden;", portal_style)
        self.assertIn(".usage-center-shell {\n  display: flex;", portal_style)
        self.assertIn("width: min(1480px, calc(100% - 48px));", portal_style)
        self.assertIn(
            ".usage-center-content {\n  display: flex;\n  flex: 1 1 auto;\n  min-height: 0;\n  overflow: hidden;",
            portal_style,
        )
        self.assertIn(".usage-account-section {\n  display: flex;\n  flex: 1 1 auto;\n  min-height: 0;", portal_style)
        self.assertIn(
            ".usage-table-wrap {\n  flex: 1 1 auto;\n  min-height: 0;\n  overflow-x: hidden;\n  overflow-y: auto;",
            portal_style,
        )
        self.assertIn("scrollbar-gutter: auto;", portal_style)
        self.assertIn("position: sticky;\n  top: 0;\n  z-index: 2;", portal_style)
        self.assertNotIn(".usage-center-content::-webkit-scrollbar", portal_style)
        self.assertNotIn(".usage-table-wrap { overflow: visible;", portal_style)
        self.assertIn("margin-top: 16px;\n  flex-direction: column;", portal_style)
        self.assertIn(".usage-toolbar-actions {\n  display: grid;\n  width: auto;", portal_style)
        self.assertIn('"windows refresh"\n    "updated updated";', portal_style)
        self.assertIn("grid-area: updated;\n  justify-self: end;", portal_style)
        self.assertIn(".usage-center-head {\n  display: flex;\n  flex: 0 0 auto;", portal_style)
        self.assertIn("padding: 18px 0 0;", portal_style)
        self.assertIn(".usage-key-card {\n  display: grid;\n  grid-template-columns: minmax(0, 1fr);", portal_style)
        self.assertIn(
            "grid-template-columns: minmax(190px, .30fr) minmax(330px, .46fr) minmax(210px, .24fr);",
            portal_style,
        )
        self.assertIn(".usage-history-help { display: contents; }", portal_style)
        self.assertLess(
            dashboard_html.index('id="codex-history-card"'),
            dashboard_html.index('id="dashboard"'),
        )

    def test_usage_center_table_fits_without_horizontal_scrolling(self):
        dashboard_html = (ROOT / "dashboard" / "index.html").read_text(encoding="utf-8")
        portal_style = (ROOT / "portal" / "app.css").read_text(encoding="utf-8")

        portal_script = (ROOT / "portal" / "my-keys.js").read_text(encoding="utf-8")

        self.assertIn('/portal/app.css?v=20260818-password-visibility-v1', dashboard_html)
        self.assertIn("overflow-x: hidden;\n  overflow-y: auto;", portal_style)
        self.assertIn(".usage-account-table {\n  width: 100%;\n  min-width: 0;", portal_style)
        self.assertIn(".usage-table-wrap::-webkit-scrollbar { width: 8px; height: 8px; }", portal_style)
        self.assertIn(".usage-table-wrap::-webkit-scrollbar-thumb {\n  background: transparent !important;", portal_style)
        self.assertNotIn(".usage-table-wrap.scrollbar-edge-active", portal_style)
        self.assertIn(".usage-table-wrap { overflow-x: hidden; overflow-y: auto;", portal_style)
        self.assertNotIn('const TABLE_SCROLL_REGION_SELECTOR', portal_script)


if __name__ == "__main__":
    unittest.main()
