import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]


class RuntimeBoundaryTests(unittest.TestCase):
    def test_legacy_topology_detection_uses_compose_not_stale_containers(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        detection = deploy[
            deploy.index("if ! TARGET_COMPOSE_SERVICES="):
            deploy.index("\nif [ \"$LEGACY_TOPOLOGY\" != true ]", deploy.index("if ! TARGET_COMPOSE_SERVICES="))
        ]
        self.assertIn("config --services", detection)
        self.assertIn("grep -Fx gateway", detection)
        self.assertIn("grep -Fx edge", detection)
        self.assertIn("true:false) LEGACY_TOPOLOGY=true", detection)
        self.assertIn("false:true) LEGACY_TOPOLOGY=false", detection)
        self.assertNotIn("LEGACY_GATEWAY_CONTAINER_ID", detection)
        self.assertNotIn("EDGE_CONTAINER_ID_BEFORE", detection)
        active_slot = deploy[
            deploy.index('if [ "$LEGACY_TOPOLOGY" != true ]'):
            deploy.index("\ncase \"$ACTIVE_GATEWAY_SLOT\"", deploy.index('if [ "$LEGACY_TOPOLOGY" != true ]'))
        ]
        self.assertIn('[ -n "$EDGE_CONTAINER_ID_BEFORE" ]', active_slot)
        self.assertIn("ACTIVE_GATEWAY_SLOT=$(env_value GATEWAY_ACTIVE_SLOT blue)", active_slot)

    def test_edge_start_failure_collects_diagnostics_before_rollback(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        diagnostic = deploy[
            deploy.index("diagnose_edge_startup() {"):
            deploy.index("\nwait_for_runtime_services()", deploy.index("diagnose_edge_startup() {"))
        ]
        self.assertIn("docker inspect --format", diagnostic)
        self.assertIn("docker logs --tail 100", diagnostic)
        self.assertIn("edge-error.log", diagnostic)
        self.assertNotIn("docker inspect $EDGE_CONTAINER_NAME", diagnostic)
        apply = deploy[
            deploy.index("apply_gateway_release() {"):
            deploy.index("\nverify_data_plane_apply_invariant()", deploy.index("apply_gateway_release() {"))
        ]
        self.assertGreaterEqual(apply.count("diagnose_edge_startup"), 2)

    def test_first_cutover_can_preserve_a_legacy_gateway_only_on_distinct_ports(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        detection = deploy[
            deploy.index("container_published_port() {"):
            deploy.index(
                '\nif [ "$LEGACY_TOPOLOGY" != true ] && [ -n "$EDGE_CONTAINER_ID_BEFORE" ]',
                deploy.index("container_published_port() {"),
            )
        ]
        self.assertIn("PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER", detection)
        self.assertIn('if [ "$LEGACY_TOPOLOGY" != true ]', detection)
        self.assertIn('LEGACY_GATEWAY_PUBLIC_PORT=$(container_published_port', detection)
        self.assertIn('LEGACY_GATEWAY_INTERNAL_PORT=$(container_published_port', detection)
        self.assertIn('[ "$LEGACY_GATEWAY_PUBLIC_PORT" = "$HEALTH_PORT" ]', detection)
        self.assertIn('[ "$LEGACY_GATEWAY_INTERNAL_PORT" = "$INTERNAL_HEALTH_PORT" ]', detection)

        apply = deploy[
            deploy.index("apply_gateway_release() {"):
            deploy.index("\nverify_data_plane_apply_invariant()", deploy.index("apply_gateway_release() {"))
        ]
        preserve = apply.index('if [ "$PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER" = true ]')
        stop = apply.index('docker stop "$LEGACY_GATEWAY_CONTAINER_NAME"', preserve)
        self.assertLess(preserve, stop)
        self.assertIn("LEGACY_GATEWAY_PRESERVED=true", apply[preserve:stop])
        self.assertIn("LEGACY_GATEWAY_STOPPED=true", apply[stop:])

    def test_steady_state_release_preserves_running_edge_ports(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        detection = deploy[
            deploy.index("STABLE_EDGE_PUBLIC_PORT="):
            deploy.index('\nif [ "$PRESERVE_LEGACY_GATEWAY_ON_FIRST_CUTOVER" = true ]')
        ]
        self.assertIn(
            'container_published_port "$EDGE_CONTAINER_NAME" 8317', detection
        )
        self.assertIn(
            'container_published_port "$EDGE_CONTAINER_NAME" 8319', detection
        )
        self.assertIn('REQUESTED_HEALTH_PORT', detection)
        self.assertIn('HEALTH_PORT=$STABLE_EDGE_PUBLIC_PORT', detection)
        self.assertIn('INTERNAL_HEALTH_PORT=$STABLE_EDGE_INTERNAL_PORT', detection)

        apply = deploy[
            deploy.index("apply_release() {"):
            deploy.index("\nif ! apply_release;")
        ]
        self.assertLess(
            apply.index("release_cli render"), apply.index("write_stable_edge_ports")
        )
        self.assertLess(
            apply.index("write_stable_edge_ports"),
            apply.index("compose config --quiet"),
        )
        self.assertLess(
            apply.index("compose config --quiet"),
            apply.index("release_cli store verify"),
        )
        writer = deploy[
            deploy.index("write_stable_edge_ports() {"):
            deploy.index("\nwrite_runtime_image_env()")
        ]
        self.assertIn('"GATEWAY_PORT": sys.argv[2]', writer)
        self.assertIn('"GATEWAY_INTERNAL_PORT": sys.argv[3]', writer)

    def test_edge_apply_never_reuses_a_stale_network_endpoint(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        apply = deploy[
            deploy.index("apply_gateway_release() {"):
            deploy.index("\nverify_data_plane_apply_invariant()", deploy.index("apply_gateway_release() {"))
        ]
        self.assertEqual(
            apply.count("compose up -d --no-deps --force-recreate edge"),
            2,
        )
        self.assertNotIn("compose up -d --no-deps edge", apply)

    def test_gateway_image_contains_only_data_plane_sources(self):
        dockerfile = (ROOT / "gateway/Dockerfile").read_text(encoding="utf-8")
        config = (ROOT / "gateway/nginx.conf").read_text(encoding="utf-8")
        self.assertNotIn("COPY portal", dockerfile)
        self.assertNotIn("COPY dashboard", dockerfile)
        for ui_path in ("/admin/", "/portal/", "/usage/limits", "/site-config.json"):
            self.assertNotIn(ui_path, config)
        self.assertIn('require("request_gate").authorize()', config)

    def test_edge_forwards_api_keys_but_never_authenticates_them(self):
        config = (ROOT / "edge/nginx.conf").read_text(encoding="utf-8")
        self.assertIn("proxy_pass http://$active_gateway_backend;", config)
        self.assertNotIn("request_gate", config)
        self.assertNotIn("keys.map", config)
        self.assertNotIn("access_by_lua", config)
        self.assertIn("location ^~ /__internal/", config)
        self.assertIn("location = /usage/api { proxy_pass http://$web_backend; }", config)
        self.assertEqual(
            config.count(
                "include /var/run/cliproxy-edge/active-gateway.conf;"
            ),
            2,
        )
        self.assertIn("gateway-blue:8317 gateway-blue:8319;", config)
        self.assertIn("gateway-green:8317 gateway-green:8319;", config)
        internal_server = config[config.index("# Host-loopback-only probes"):]
        self.assertIn(
            "proxy_pass http://$active_gateway_internal_backend;",
            internal_server,
        )
        self.assertNotIn(
            "proxy_pass http://$active_gateway_backend;",
            internal_server,
        )
        self.assertNotIn("/var/run/cliproxy-edge/*.conf", config)
        public_server = config[: config.index("# Host-loopback-only probes")]
        self.assertIn("return 404;", public_server)

    def test_public_data_plane_is_an_explicit_allowlist(self):
        gateway = (ROOT / "gateway/nginx.conf").read_text(encoding="utf-8")
        expected = (
            "~^/v1(?:/|$) 1;",
            "~^/v1beta(?:/|$) 1;",
            "~^/backend-api/codex(?:/|$) 1;",
            "~^/api/provider(?:/|$) 1;",
            "~^/v1internal:method$ 1;",
        )

        allowlist_start = gateway.index("map $uri $public_api_route_allowed")
        allowlist_end = gateway.index("\n    }", allowlist_start)
        allowlist = gateway[allowlist_start:allowlist_end]
        self.assertIn("default 0;", allowlist)
        for route in expected:
            self.assertIn(route, allowlist)
        for forbidden in (
            "/v0/management",
            "/management.html",
            "/healthz",
            "/keep-alive",
            "/anthropic/callback",
            "/codex/callback",
            "/google/callback",
            "/antigravity/callback",
            "/xai/callback",
        ):
            self.assertNotIn(forbidden, allowlist)
        public_proxy = gateway[gateway.index("location / {"):]
        self.assertIn("if ($public_api_route_allowed = 0)", public_proxy)
        self.assertIn("return 404;", public_proxy)

    def test_web_owns_static_surfaces_and_strips_control_credentials(self):
        dockerfile = (ROOT / "web/Dockerfile").read_text(encoding="utf-8")
        config = (ROOT / "web/nginx.conf").read_text(encoding="utf-8")
        self.assertIn("COPY portal", dockerfile)
        self.assertIn("COPY dashboard", dockerfile)
        self.assertIn('proxy_set_header X-Management-Key "";', config)
        self.assertIn('proxy_set_header Authorization "";', config)
        self.assertIn("location = /usage/api", config)
        self.assertIn("me/key/rotate", config)
        self.assertIn("location /admin/", config)
        self.assertIn("view-state-utils\\.js", config)
        self.assertIn("set $admin_backend admin:8318;", config)
        self.assertIn("proxy_pass http://$admin_backend;", config)
        self.assertNotIn("proxy_pass http://admin:8318;", config)
        self.assertNotIn("/v1/", config)

    def test_release_uses_inactive_slot_before_edge_switch_and_drain(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("apply_gateway_release() {")
        end = deploy.index("\nverify_data_plane_apply_invariant()", start)
        apply = deploy[start:end]
        self.assertLess(
            apply.index('compose up -d --no-deps "$INACTIVE_GATEWAY_SERVICE"'),
            apply.index('wait_for_gateway_snapshots "$INACTIVE_GATEWAY_SERVICE"'),
        )
        self.assertLess(
            apply.index('verify_gateway_routes_on_service "$INACTIVE_GATEWAY_SERVICE"'),
            apply.index('switch_gateway_slot "$INACTIVE_GATEWAY_SLOT"'),
        )
        self.assertIn(
            'verify_gateway_routes_on_service "$INACTIVE_GATEWAY_SERVICE" || return 1',
            apply,
        )
        switch_start = deploy.index("switch_gateway_slot() {")
        switch_end = deploy.index("\napply_web_release()", switch_start)
        switch = deploy[switch_start:switch_end]
        self.assertLess(switch.index("reload_edge"), switch.index("drain_gateway_slot"))
        self.assertLess(switch.index("drain_gateway_slot"), switch.index('compose stop "$OLD_SERVICE"'))
        self.assertIn("本次发布拒绝标记成功", switch)
        self.assertIn("return 1", switch)

    def test_route_probe_exercises_internal_and_public_quota_paths(self):
        probe = (ROOT / "scripts/gateway_release_probe.py").read_text(encoding="utf-8")
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        self.assertIn('"/__internal/probe/models"', probe)
        self.assertIn('"/v1/models"', probe)
        for path in (
            "/gateway-release-unknown-path",
            "/v0/management/auth-files",
            "/management.html",
            "/codex/callback",
            "/healthz",
        ):
            self.assertIn('"{}"'.format(path), probe)
        self.assertIn("verify_blocked_public_paths", probe)
        self.assertIn("status != 404", probe)
        self.assertIn("weekly_user_token_quota_exceeded", probe)
        self.assertIn("--public-url \"http://$SERVICE:8317\"", deploy)
        self.assertIn("--internal-url \"http://$SERVICE:8319\"", deploy)

    def test_release_preserves_disabled_account_services(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        collector = (ROOT / "admin/usage_collector.py").read_text(encoding="utf-8")
        probe = (ROOT / "scripts/gateway_release_probe.py").read_text(encoding="utf-8")

        wait_start = deploy.index("wait_for_runtime_services() {")
        wait = deploy[
            wait_start:deploy.index("\ncompose_network_name()", wait_start)
        ]
        self.assertIn('root / "state" / "public" / "accounts.json"', wait)
        self.assertIn('account.get("group_enabled") is False', wait)
        self.assertIn("expected.difference_update(disabled_services)", wait)

        ensure_start = deploy.index("ensure_and_verify_business_cpas() {")
        ensure = deploy[
            ensure_start:deploy.index("\nverify_gateway_routes()", ensure_start)
        ]
        self.assertIn('accounts[account].get("group_enabled") is False', ensure)
        self.assertIn('app.compose("stop", service)', ensure)
        self.assertLess(
            ensure.index('accounts[account].get("group_enabled") is False'),
            ensure.index(
                'app._compose_with_image(cliproxy_image, "up", "-d", "--no-deps", service)'
            ),
        )

        self.assertIn('metadata.get("group_enabled") is False', collector)
        self.assertIn('metadata.get("group_enabled") is not False', probe)

    def test_builds_validate_all_nginx_images_before_runtime_cutover(self):
        for relative, expected in (
            ("edge/Dockerfile", "openresty -t"),
            ("gateway/Dockerfile", "openresty -t"),
            ("web/Dockerfile", "nginx -t"),
        ):
            self.assertIn(expected, (ROOT / relative).read_text(encoding="utf-8"))
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        self.assertLess(
            deploy.index('"$EDGE_RUNTIME_IMAGE" openresty -t'),
            deploy.index("apply_release() {"),
        )

    def test_regular_release_refuses_to_recreate_edge(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        guard = 'if [ "$EDGE_APPLY_REQUIRED" = true ] && [ "$ALLOW_EDGE_RECREATE" != true ]'
        preflight_restore = (
            "if environment_mutated and not apply_started:"
        )
        self.assertIn('ALLOW_EDGE_RECREATE=${ALLOW_EDGE_RECREATE:-false}', deploy)
        self.assertIn(guard, deploy)
        self.assertIn("常规发布已停止", deploy)
        self.assertIn("PRE_APPLY_ENV_MUTATED=false", deploy)
        self.assertNotIn("PRE_APPLY_ENV_MUTATED=true", deploy)
        self.assertIn("APPLY_RELEASE_STARTED=true", deploy)
        self.assertIn(preflight_restore, deploy)
        self.assertIn("edge-runtime-equivalent", deploy)
        self.assertIn("PREVIOUS_EDGE_DESIRED_CONFIG_HASH", deploy)
        self.assertIn("EDGE_RUNTIME_REUSED=true", deploy)
        self.assertIn("assert_edge_unchanged()", deploy)
        invariant_start = deploy.index("verify_data_plane_apply_invariant() {")
        invariant = deploy[
            invariant_start:deploy.index("\nrestore_control_services()", invariant_start)
        ]
        self.assertIn('if [ "$EDGE_RUNTIME_REUSED" = true ]', invariant)
        self.assertIn("assert_edge_unchanged || return 1", invariant)
        self.assertLess(deploy.index("edge-runtime-equivalent"), deploy.index(guard))
        self.assertLess(deploy.index(guard), deploy.index("write_deploy_root_env()"))

    def test_host_loopback_acceptance_runs_in_host_network_namespace(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        http_get = deploy[deploy.index("http_get() {") : deploy.index("invalid_key_status() {")]
        invalid_key = deploy[
            deploy.index("invalid_key_status() {") : deploy.index("wait_for_service() {")
        ]
        invocation = 'docker run --rm --network host "$ADMIN_RUNTIME_IMAGE" python3 -c'
        self.assertIn(invocation, http_get)
        self.assertIn(invocation, invalid_key)
        self.assertNotIn("\n  python3 -c", http_get)
        self.assertNotIn("\n  python3 -c", invalid_key)

    def test_rollback_drains_new_slot_before_stopping_it(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("restore_release() {")
        end = deploy.index("\napply_release()", start)
        restore = deploy[start:end]
        drain = 'gateway_inflight_total "$ORIGINAL_INACTIVE_GATEWAY_SERVICE"'
        stop = 'compose stop "$ORIGINAL_INACTIVE_GATEWAY_SERVICE"'
        self.assertIn(drain, restore)
        self.assertLess(restore.index(drain), restore.index(stop))
        self.assertIn("保持运行直至人工确认", restore)

    def test_rollback_restores_the_previous_cpa_running_set(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        self.assertIn("PREVIOUS_CONFIGURED_CPA_SERVICES=$(compose config --services", deploy)
        self.assertIn("PREVIOUS_RUNNING_CPA_SERVICES=$(compose ps --status running", deploy)

        helper_start = deploy.index("restore_previous_business_cpas() {")
        helper_end = deploy.index("\nrestore_release()", helper_start)
        helper = deploy[helper_start:helper_end]
        self.assertIn("for SERVICE in $PREVIOUS_CONFIGURED_CPA_SERVICES", helper)
        self.assertIn("for RUNNING_SERVICE in $PREVIOUS_RUNNING_CPA_SERVICES", helper)
        self.assertIn('compose up -d --no-deps "$SERVICE"', helper)
        self.assertIn('compose stop "$SERVICE"', helper)

        restore_start = deploy.index("restore_release() {")
        restore_end = deploy.index("\napply_release()", restore_start)
        restore = deploy[restore_start:restore_end]
        self.assertIn("restore_previous_business_cpas || return 1", restore)
        self.assertNotIn("ensure_and_verify_business_cpas || return 1", restore)

        with tempfile.TemporaryDirectory() as directory:
            log = Path(directory) / "compose.log"
            harness = r'''
PREVIOUS_CONFIGURED_CPA_SERVICES="cliproxy-alpha cliproxy-beta"
PREVIOUS_RUNNING_CPA_SERVICES="cliproxy-alpha"
COMPOSE_LOG=$1
compose() { printf '%s\n' "$*" >> "$COMPOSE_LOG"; }
restore_previous_business_cpas
'''
            subprocess.run(
                ["sh", "-s", "--", str(log)],
                input=helper + "\n" + harness,
                text=True,
                check=True,
            )
            self.assertEqual(
                log.read_text(encoding="utf-8").splitlines(),
                [
                    "up -d --no-deps cliproxy-alpha",
                    "stop cliproxy-beta",
                ],
            )

    def test_switch_attempt_is_recorded_before_edge_reload(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("switch_gateway_slot() {")
        end = deploy.index("\napply_web_release()", start)
        switch = deploy[start:end]
        self.assertLess(switch.index("GATEWAY_SWITCHED=true"), switch.index("reload_edge"))

    def test_critical_nested_steps_fail_explicitly_under_if_not_context(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        for expected in (
            'wait_for_gateway_snapshots "$INACTIVE_GATEWAY_SERVICE" || return 1',
            'verify_gateway_routes_on_service "$INACTIVE_GATEWAY_SERVICE" || return 1',
            'switch_gateway_slot "$INACTIVE_GATEWAY_SLOT" || return 1',
            'reload_edge || return 1',
            'verify_gateway_routes || return 1',
            'write_deploy_root_env || return 1',
        ):
            self.assertIn(expected, deploy)

    def test_inactive_snapshot_failure_prevents_route_probe_and_switch(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("apply_gateway_release() {")
        end = deploy.index("\nverify_data_plane_apply_invariant()", start)
        function = deploy[start:end]
        harness = r'''
LEGACY_TOPOLOGY=false
EDGE_APPLY_REQUIRED=false
GATEWAY_APPLY_REQUIRED=true
INACTIVE_GATEWAY_SERVICE=gateway-green
INACTIVE_GATEWAY_SLOT=green
compose() { return 0; }
wait_for_gateway_snapshots() { return 23; }
verify_gateway_routes_on_service() { echo unsafe-probe; return 0; }
switch_gateway_slot() { echo unsafe-switch; return 0; }
if apply_gateway_release; then
  echo unsafe-success
  exit 91
fi
exit 0
'''
        result = subprocess.run(
            ["sh"],
            input=function + "\n" + harness,
            text=True,
            capture_output=True,
            check=True,
        )
        self.assertNotIn("unsafe", result.stdout)

    def test_ambiguous_reload_failure_is_recorded_for_rollback(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("switch_gateway_slot() {")
        end = deploy.index("\napply_web_release()", start)
        function = deploy[start:end]
        harness = r'''
ACTIVE_GATEWAY_SERVICE=gateway-blue
GATEWAY_SWITCHED=false
write_active_gateway_slot() { return 0; }
reload_edge() { return 24; }
wait_for_service() { echo unsafe-wait; return 0; }
if switch_gateway_slot green; then
  echo unsafe-success
  exit 92
fi
test "$GATEWAY_SWITCHED" = true
exit 0
'''
        result = subprocess.run(
            ["sh"],
            input=function + "\n" + harness,
            text=True,
            capture_output=True,
            check=True,
        )
        self.assertNotIn("unsafe", result.stdout)

    def test_web_only_apply_does_not_include_edge_or_gateway(self):
        deploy = (ROOT / "scripts/deploy-release.sh").read_text(encoding="utf-8")
        start = deploy.index("apply_web_release() {")
        end = deploy.index("\napply_gateway_release()", start)
        helper = deploy[start:end]
        self.assertIn('if [ "$WEB_APPLY_REQUIRED" = true ]', helper)
        self.assertIn("compose up -d --no-deps web", helper)
        self.assertNotIn("edge", helper)
        self.assertNotIn("gateway", helper)


if __name__ == "__main__":
    unittest.main()
