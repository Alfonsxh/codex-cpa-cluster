#!/usr/bin/env sh
set -eu

# 镜像内代码与宿主机持久化根目录可以分离。
APP_ROOT=${CLIPROXY_APP_ROOT:-$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)}
DATA_ROOT=${CLIPROXY_ROOT:-$APP_ROOT}
exec python3 "$APP_ROOT/scripts/cliproxy.py" --root "$DATA_ROOT" "$@"
