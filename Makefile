SHELL := /bin/sh

VERSION ?=
PLATFORM ?= linux/amd64
IMAGE_PREFIXES ?=
HARBOR_PREFIX ?=
DOCKERHUB_PREFIX ?=
GHCR_PREFIX ?=
IMAGE_PREFIX ?=
UPDATE_IMAGE_PREFIX ?= $(IMAGE_PREFIX)
DEPLOY_ROOT ?= /opt/codex-cpa-cluster
HEALTH_PORT ?=
CONFIG_PROFILE ?=
ALLOW_EDGE_RECREATE ?= false
RELEASE_ARCHIVE ?= dist/codex-cpa-cluster-$(VERSION).tar.gz
GH_REPO ?= Alfonsxh/codex-cpa-cluster
GIT_REMOTE ?= origin
RELEASE_BRANCH ?= main
COMPOSE_RUNTIME_ENV := $(if $(wildcard state/compose.env),state/compose.env,compose.env.example)
V2_TEST_PROJECT ?= codex-cpa-v2-test
V2_TARGET_ENV ?= v2-target.env
V1_COMPARE_ENV ?= v1-compare.env
FRONTEND_DEV_UPSTREAM ?= http://127.0.0.1:8318

.PHONY: help verify generate-api check-generated-api frontend-dev frontend-dev-admin frontend-dev-usage frontend-dev-portal dev-config dev-build dev-up v1-compare-config v1-compare-verify-images v1-compare-up v1-compare-smoke v1-compare-ps v1-compare-down v2-test-config v2-test-build v2-test-up v2-test-smoke v2-test-down v2-test-faults v2-target-config v2-target-pull v2-target-verify-images v2-target-ownership-status v2-target-activate v2-target-up-core v2-target-up-writers v2-target-up-notifications v2-target-smoke v2-target-ps v2-target-down v2-lease-rehearsal v2-worker-lease-rehearsal v2-worker-process-rehearsal privacy-check package images publish publish-harbor publish-dockerhub publish-ghcr publish-all release-check release deploy

help:
	@printf '%s\n' \
	  'make verify' \
	  'make generate-api' \
	  'make check-generated-api' \
	  'make frontend-dev [FRONTEND_DEV_UPSTREAM=http://test-host:18317]  # Admin，读写代理' \
	  'make frontend-dev-usage [FRONTEND_DEV_UPSTREAM=http://test-host:18317]' \
	  'make frontend-dev-portal [FRONTEND_DEV_UPSTREAM=http://test-host:18317]' \
	  'make dev-build' \
	  'make dev-up' \
	  'make v1-compare-up V1_COMPARE_ENV=/path/to/v1-compare.env' \
	  'make v1-compare-smoke V1_COMPARE_ENV=/path/to/v1-compare.env' \
	  'make v2-test-build' \
	  'make v2-test-up' \
	  'make v2-test-smoke' \
	  'make v2-test-down' \
	  'make v2-test-faults' \
	  'make v2-target-config V2_TARGET_ENV=/path/to/v2-target.env' \
	  'make v2-lease-rehearsal' \
	  'make v2-worker-lease-rehearsal' \
	  'make v2-worker-process-rehearsal' \
	  'make privacy-check' \
	  'make package VERSION=v1.0.0' \
	  'make images VERSION=v1.0.0 [PLATFORM=linux/amd64]' \
	  'make publish VERSION=v1.0.0 IMAGE_PREFIXES="registry.example.com/team docker.io/user"' \
	  'make publish-harbor VERSION=v1.0.0 HARBOR_PREFIX=registry.example.com/team' \
	  'make publish-dockerhub VERSION=v1.0.0 DOCKERHUB_PREFIX=docker.io/user' \
	  'make publish-ghcr VERSION=v1.0.0 GHCR_PREFIX=ghcr.io/owner' \
	  'make publish-all VERSION=v1.0.0 HARBOR_PREFIX=... DOCKERHUB_PREFIX=...' \
	  'make release-check VERSION=v1.1.0 IMAGE_PREFIX=ghcr.io/owner' \
	  'make release VERSION=v1.1.0 IMAGE_PREFIX=ghcr.io/owner'

verify:
	sh scripts/verify.sh

generate-api:
	sh scripts/generate-api.sh

check-generated-api:
	sh scripts/check-generated-api.sh

frontend-dev: frontend-dev-admin

frontend-dev-admin:
	@printf 'Admin: http://127.0.0.1:5173/admin/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"
	CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev

frontend-dev-usage:
	@printf 'Usage: http://127.0.0.1:5174/usage/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"
	CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev:usage

frontend-dev-portal:
	@printf 'Portal: http://127.0.0.1:5175/portal/ -> %s（读写）\n' "$(FRONTEND_DEV_UPSTREAM)"
	CPA_DEV_PROXY_TARGET="$(FRONTEND_DEV_UPSTREAM)" npm --prefix frontend run dev:portal

dev-config:
	docker compose --env-file .env --env-file $(COMPOSE_RUNTIME_ENV) -f docker-compose.yml -f docker-compose.dev.yml config --quiet

dev-build:
	docker compose --env-file .env --env-file $(COMPOSE_RUNTIME_ENV) -f docker-compose.yml -f docker-compose.dev.yml build admin web gateway-blue edge

dev-up:
	docker compose --env-file .env --env-file state/compose.env -f docker-compose.yml -f docker-compose.dev.yml -f compose.accounts.yml up -d

v1-compare-config:
	V1_COMPARE_ENV_FILE="$(V1_COMPARE_ENV)" sh scripts/deploy-v1-compare-target.sh config

v1-compare-verify-images:
	V1_COMPARE_ENV_FILE="$(V1_COMPARE_ENV)" sh scripts/deploy-v1-compare-target.sh verify-images

v1-compare-up:
	V1_COMPARE_ENV_FILE="$(V1_COMPARE_ENV)" sh scripts/deploy-v1-compare-target.sh up

v1-compare-smoke:
	V1_COMPARE_ENV_FILE="$(V1_COMPARE_ENV)" sh scripts/deploy-v1-compare-target.sh smoke

v1-compare-ps:
	V1_COMPARE_ENV_FILE="$(V1_COMPARE_ENV)" sh scripts/deploy-v1-compare-target.sh ps

v1-compare-down:
	V1_COMPARE_ENV_FILE="$(V1_COMPARE_ENV)" sh scripts/deploy-v1-compare-target.sh down

v2-test-config:
	docker compose -p $(V2_TEST_PROJECT) -f docker-compose.v2-test.yml config --quiet

v2-test-build: v2-test-config
	docker compose -p $(V2_TEST_PROJECT) -f docker-compose.v2-test.yml build

v2-test-up: v2-test-config
	docker compose -p $(V2_TEST_PROJECT) -f docker-compose.v2-test.yml up -d --wait

v2-test-smoke:
	V2_TEST_PROJECT="$(V2_TEST_PROJECT)" sh scripts/v2-test-smoke.sh

v2-test-down:
	docker compose -p $(V2_TEST_PROJECT) -f docker-compose.v2-test.yml down --remove-orphans

v2-test-faults:
	sh scripts/v2-test-faults.sh

v2-target-config:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh config

v2-target-pull:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh pull

v2-target-verify-images:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh verify-images

v2-target-ownership-status:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh ownership-status

v2-target-activate:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh activate

v2-target-up-core:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh up-core

v2-target-up-writers:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh up-writers

v2-target-up-notifications:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh up-notifications

v2-target-smoke:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh smoke

v2-target-ps:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh ps

v2-target-down:
	V2_ENV_FILE="$(V2_TARGET_ENV)" sh scripts/deploy-v2-target.sh down

v2-lease-rehearsal:
	go test -count=1 -run '^TestCrossLanguageWriterLeaseTransfersV1ToV2AndBack$$' ./internal/ownership

v2-worker-lease-rehearsal:
	go test -count=1 -run '^TestGoWorkerLeaseGroupTransfersAllScopesAndRejectsDuplicate$$' ./internal/ownership

v2-worker-process-rehearsal:
	sh scripts/v2-worker-process-rehearsal.sh

privacy-check:
	python3 scripts/check-public-release.py --root .

package: privacy-check
	@test -n "$(VERSION)" || { echo 'VERSION 不能为空，例如 VERSION=v1.0.0' >&2; exit 1; }
	sh scripts/package-release.sh "$(RELEASE_ARCHIVE)"

images: privacy-check
	@test -n "$(VERSION)" || { echo 'VERSION 不能为空，例如 VERSION=v1.0.0' >&2; exit 1; }
	VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" sh scripts/release-images.sh build

publish: privacy-check
	@test -n "$(VERSION)" || { echo 'VERSION 不能为空，例如 VERSION=v1.0.0' >&2; exit 1; }
	@test -n "$(IMAGE_PREFIXES)" || { echo 'IMAGE_PREFIXES 不能为空' >&2; exit 1; }
	VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(IMAGE_PREFIXES)" sh scripts/release-images.sh publish

publish-harbor:
	@test -n "$(HARBOR_PREFIX)" || { echo 'HARBOR_PREFIX 不能为空' >&2; exit 1; }
	$(MAKE) publish VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(HARBOR_PREFIX)"

publish-dockerhub:
	@test -n "$(DOCKERHUB_PREFIX)" || { echo 'DOCKERHUB_PREFIX 不能为空' >&2; exit 1; }
	$(MAKE) publish VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(DOCKERHUB_PREFIX)"

publish-ghcr:
	@test -n "$(GHCR_PREFIX)" || { echo 'GHCR_PREFIX 不能为空' >&2; exit 1; }
	$(MAKE) publish VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(GHCR_PREFIX)"

publish-all:
	@test -n "$(HARBOR_PREFIX)" || { echo 'HARBOR_PREFIX 不能为空' >&2; exit 1; }
	@test -n "$(DOCKERHUB_PREFIX)" || { echo 'DOCKERHUB_PREFIX 不能为空' >&2; exit 1; }
	$(MAKE) publish VERSION="$(VERSION)" PLATFORM="$(PLATFORM)" IMAGE_PREFIXES="$(HARBOR_PREFIX) $(DOCKERHUB_PREFIX)"

release-check:
	VERSION="$(VERSION)" \
	IMAGE_PREFIX="$(IMAGE_PREFIX)" \
	PLATFORM="$(PLATFORM)" \
	GH_REPO="$(GH_REPO)" \
	GIT_REMOTE="$(GIT_REMOTE)" \
	RELEASE_BRANCH="$(RELEASE_BRANCH)" \
	  sh scripts/local-release.sh check

release:
	VERSION="$(VERSION)" \
	IMAGE_PREFIX="$(IMAGE_PREFIX)" \
	PLATFORM="$(PLATFORM)" \
	GH_REPO="$(GH_REPO)" \
	GIT_REMOTE="$(GIT_REMOTE)" \
	RELEASE_BRANCH="$(RELEASE_BRANCH)" \
	  sh scripts/local-release.sh publish

deploy: package
	@test -n "$(IMAGE_PREFIX)" || { echo 'IMAGE_PREFIX 不能为空' >&2; exit 1; }
	RELEASE_VERSION="$(VERSION)" \
	RELEASE_IMAGE_PREFIX="$(IMAGE_PREFIX)" \
	RELEASE_METADATA_IMAGE="$(UPDATE_IMAGE_PREFIX)/codex-cpa-release:latest" \
	RELEASE_COMMIT_SHA="$$(git rev-parse HEAD)" \
	ALLOW_EDGE_RECREATE="$(ALLOW_EDGE_RECREATE)" \
	sh scripts/deploy-release.sh "$(RELEASE_ARCHIVE)" "$(DEPLOY_ROOT)" "$(HEALTH_PORT)" "$(CONFIG_PROFILE)"
