SHELL := /bin/sh

# The root Makefile is intentionally limited to local frontend development.
# Test/build/release/deployment commands live in scripts/build.mk.
-include frontend/.env

FRONTEND_DEV_UPSTREAM ?= $(CPA_DEV_PROXY_TARGET)
ifeq ($(strip $(FRONTEND_DEV_UPSTREAM)),)
FRONTEND_DEV_UPSTREAM := http://127.0.0.1:8318
endif

.PHONY: help frontend-dev frontend-dev-all frontend-dev-admin frontend-dev-usage frontend-dev-portal

help:
	@printf '%s\n' \
	  'make frontend-dev-all  # 同时启动三个前端；后端读取 frontend/.env' \
	  'make frontend-dev [FRONTEND_DEV_UPSTREAM=http://test-host:18317]  # Admin，读写代理' \
	  'make frontend-dev-usage [FRONTEND_DEV_UPSTREAM=http://test-host:18317]' \
	  'make frontend-dev-portal [FRONTEND_DEV_UPSTREAM=http://test-host:18317]'

frontend-dev: frontend-dev-admin

frontend-dev-all:
	@set -eu; \
	  pids=''; \
	  cleanup() { \
	    trap - 0 2 15; \
	    for pid in $$pids; do kill "$$pid" 2>/dev/null || true; done; \
	    for pid in $$pids; do wait "$$pid" 2>/dev/null || true; done; \
	  }; \
	  trap cleanup 0; \
	  trap 'exit 130' 2; \
	  trap 'exit 143' 15; \
	  printf 'Admin:  http://127.0.0.1:5173/admin/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"; \
	  printf 'Usage:  http://127.0.0.1:5174/usage/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"; \
	  printf 'Portal: http://127.0.0.1:5175/portal/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"; \
	  CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev & pids="$$pids $$!"; \
	  CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev:usage & pids="$$pids $$!"; \
	  CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev:portal & pids="$$pids $$!"; \
	  while :; do \
	    for pid in $$pids; do \
	      if ! kill -0 "$$pid" 2>/dev/null; then \
	        set +e; wait "$$pid"; status=$$?; set -e; exit "$$status"; \
	      fi; \
	    done; \
	    sleep 1; \
	  done

frontend-dev-admin:
	@printf 'Admin: http://127.0.0.1:5173/admin/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"
	CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev

frontend-dev-usage:
	@printf 'Usage: http://127.0.0.1:5174/usage/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"
	CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev:usage

frontend-dev-portal:
	@printf 'Portal: http://127.0.0.1:5175/portal/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"
	CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev:portal
