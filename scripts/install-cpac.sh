#!/usr/bin/env sh
set -eu

REPOSITORY=${CPAC_GITHUB_REPOSITORY:-Alfonsxh/codex-cpa-cluster}
DESTINATION=${CPAC_INSTALL_PATH:-/usr/local/bin/cpac}

[ "$(id -u)" -eq 0 ] || { echo "请使用 sudo 运行 cpac 安装器" >&2; exit 1; }
for command in curl sha256sum; do
  command -v "$command" >/dev/null 2>&1 || { echo "缺少安装依赖：$command" >&2; exit 1; }
done

work_directory=$(mktemp -d "${TMPDIR:-/var/tmp}/cpac-install.XXXXXX")
trap 'rm -rf -- "$work_directory"' EXIT HUP INT TERM
latest_url=$(curl -fsSL --retry 3 -o /dev/null -w '%{url_effective}' \
  "https://github.com/$REPOSITORY/releases/latest")
version=${latest_url##*/}
printf '%s' "$version" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$' \
  || { echo "无法确定有效的最新版本" >&2; exit 1; }
base_url="https://github.com/$REPOSITORY/releases/download/$version"
curl -fL --retry 3 "$base_url/cpac-linux-amd64" -o "$work_directory/cpac-linux-amd64"
curl -fL --retry 3 "$base_url/SHA256SUMS" -o "$work_directory/SHA256SUMS"
grep -Eq '^[0-9a-f]{64}  cpac-linux-amd64$' "$work_directory/SHA256SUMS" \
  || { echo "SHA256SUMS 缺少 cpac-linux-amd64" >&2; exit 1; }
(
  cd "$work_directory"
  sha256sum -c --ignore-missing SHA256SUMS
)
chmod 0755 "$work_directory/cpac-linux-amd64"
mkdir -p -- "$(dirname -- "$DESTINATION")"
install -o root -g root -m 0755 "$work_directory/cpac-linux-amd64" "$DESTINATION"
printf 'cpac 已安装：%s（%s）\n' "$DESTINATION" "$version"
