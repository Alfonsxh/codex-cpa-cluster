import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).parents[1]


class WebNginxConfigurationTests(unittest.TestCase):
    def test_admin_static_assets_are_copied_and_served_by_web(self):
        dockerfile = (ROOT / "web/Dockerfile").read_text(encoding="utf-8")
        config = (ROOT / "web/nginx.conf").read_text(encoding="utf-8")

        self.assertIn(
            "COPY admin/static /usr/share/nginx/html/admin",
            dockerfile,
        )
        html_route = config[
            config.index("location = /admin/ {"):
            config.index("location = /admin/index.html {")
        ]
        self.assertIn("try_files /admin/index.html =404;", html_route)
        self.assertIn('add_header Cache-Control "no-cache" always;', html_route)
        self.assertIn("add_header Content-Security-Policy", html_route)

        asset_route = config[
            config.index(
                "location ~ ^/admin/(?:app\\.js|app\\.css|monitor-utils\\.js|view-state-utils\\.js)$"
            ):
            config.index("location /admin/ { return 404; }")
        ]
        self.assertIn("try_files $uri =404;", asset_route)
        admin_html = (ROOT / "admin/static/index.html").read_text(encoding="utf-8")
        static_admin_assets = set(
            re.findall(r'(?:src|href)="/admin/([^"?]+)', admin_html)
        ) - {"reasoning-effort-colors.css"}
        for asset in static_admin_assets:
            self.assertIn(re.escape(asset).replace(r"\-", "-"), asset_route)
        self.assertIn("$admin_static_cache_control", asset_route)
        self.assertIn('"public, max-age=31536000, immutable"', config)
        self.assertIn('"" "no-cache";', config)

    def test_admin_dynamic_routes_remain_proxied_and_protected(self):
        config = (ROOT / "web/nginx.conf").read_text(encoding="utf-8")
        dynamic_css = config[
            config.index("location = /admin/reasoning-effort-colors.css {"):
            config.index("location ^~ /admin/api/ {")
        ]
        api = config[
            config.index("location ^~ /admin/api/ {"):
            config.index("location = /admin/ {")
        ]

        self.assertIn("proxy_pass http://$admin_backend;", dynamic_css)
        self.assertIn("limit_req zone=admin_api_per_ip burst=60 nodelay;", api)
        self.assertIn("limit_conn admin_api_conn_per_ip 32;", api)
        self.assertIn("proxy_read_timeout 3600s;", api)
        self.assertIn("proxy_send_timeout 3600s;", api)
        self.assertIn("proxy_pass http://$admin_backend;", api)


@unittest.skipUnless(shutil.which("docker"), "Docker CLI is unavailable")
class NginxSyntaxTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        info = subprocess.run(
            ["docker", "info"], capture_output=True, text=True, timeout=10
        )
        if info.returncode != 0:
            raise unittest.SkipTest("Docker daemon is unavailable")

    @staticmethod
    def _base_image(dockerfile, argument):
        for line in dockerfile.read_text(encoding="utf-8").splitlines():
            if line.startswith("ARG {}=".format(argument)):
                return line.split("=", 1)[1]
        raise AssertionError("missing image argument {}".format(argument))

    def _run_config_test(self, image, config, destination, command, mounts=()):
        invocation = [
            "docker", "run", "--rm",
            "-v", "{}:{}:ro".format(config, destination),
        ]
        for source, target in mounts:
            invocation.extend(["-v", "{}:{}:ro".format(source, target)])
        invocation.extend([image, *command])
        subprocess.run(invocation, check=True, timeout=60)

    def test_edge_gateway_and_web_nginx_configs_parse(self):
        edge_image = self._base_image(ROOT / "edge/Dockerfile", "EDGE_BASE_IMAGE")
        gateway_image = self._base_image(ROOT / "gateway/Dockerfile", "GATEWAY_BASE_IMAGE")
        web_image = self._base_image(ROOT / "web/Dockerfile", "WEB_BASE_IMAGE")
        with tempfile.TemporaryDirectory() as temporary:
            state = Path(temporary) / "edge-state"
            state.mkdir()
            (state / "active-gateway.conf").write_text(
                "set $active_gateway_backend gateway-blue:8317;\n",
                encoding="utf-8",
            )
            self._run_config_test(
                edge_image,
                ROOT / "edge/nginx.conf",
                "/usr/local/openresty/nginx/conf/nginx.conf",
                ["sh", "-c", "mkdir -p /var/log/cliproxy && openresty -t"],
                mounts=((state, "/var/run/cliproxy-edge"),),
            )
        self._run_config_test(
            gateway_image,
            ROOT / "gateway/nginx.conf",
            "/usr/local/openresty/nginx/conf/nginx.conf",
            ["sh", "-c", "mkdir -p /var/log/cliproxy && openresty -t"],
            mounts=(
                (
                    ROOT / "gateway/gateway_state.lua",
                    "/usr/local/openresty/nginx/conf/custom/gateway_state.lua",
                ),
                (
                    ROOT / "gateway/request_gate.lua",
                    "/usr/local/openresty/nginx/conf/custom/request_gate.lua",
                ),
            ),
        )
        self._run_config_test(
            web_image,
            ROOT / "web/nginx.conf",
            "/etc/nginx/nginx.conf",
            ["nginx", "-t"],
        )


if __name__ == "__main__":
    unittest.main()
