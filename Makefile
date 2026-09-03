SHELL := /bin/sh

VERSION ?=
PLATFORM ?= linux/amd64
IMAGE_PREFIXES ?=
HARBOR_PREFIX ?=
DOCKERHUB_PREFIX ?=
GHCR_PREFIX ?=
IMAGE_PREFIX ?=
RELEASE_ARCHIVE ?= dist/codex-cpa-cluster-$(VERSION).tar.gz
GH_REPO ?= Alfonsxh/codex-cpa-cluster
GIT_REMOTE ?= origin
RELEASE_BRANCH ?= main
TEST_PROJECT ?= codex-cpa-test
TARGET_ENV ?= target.env
FRONTEND_DEV_UPSTREAM ?= http://127.0.0.1:8318

.PHONY: help verify run-test generate-api check-generated-api frontend-dev frontend-dev-admin frontend-dev-usage frontend-dev-portal test-config test-build test-up test-smoke test-down test-faults target-config target-pull target-verify-images target-ownership-status target-activate target-up-core target-up-writers target-up-notifications target-smoke target-ps target-down lease-rehearsal worker-lease-rehearsal privacy-check package images publish publish-harbor publish-dockerhub publish-ghcr publish-all release-check release run

help:
	@printf '%s\n' \
	  'make verify' \
	  'make run-test' \
	  'make generate-api' \
	  'make check-generated-api' \
	  'make frontend-dev [FRONTEND_DEV_UPSTREAM=http://test-host:18317]  # Admin，读写代理' \
	  'make frontend-dev-usage [FRONTEND_DEV_UPSTREAM=http://test-host:18317]' \
	  'make frontend-dev-portal [FRONTEND_DEV_UPSTREAM=http://test-host:18317]' \
	  'make test-build' \
	  'make test-up' \
	  'make test-smoke' \
	  'make test-down' \
	  'make test-faults' \
	  'make target-config TARGET_ENV=/path/to/target.env' \
	  'make target-verify-images TARGET_ENV=/path/to/target.env' \
	  'make target-ownership-status TARGET_ENV=/path/to/target.env' \
	  'make run TARGET_ENV=/path/to/target.env' \
	  'make lease-rehearsal' \
	  'make worker-lease-rehearsal' \
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

run-test:
	sh scripts/test-run.sh

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

test-config:
	docker compose -p $(TEST_PROJECT) -f docker-compose.test.yml config --quiet

test-build: test-config
	docker compose -p $(TEST_PROJECT) -f docker-compose.test.yml build

test-up: test-config
	docker compose -p $(TEST_PROJECT) -f docker-compose.test.yml up -d --wait

test-smoke:
	TEST_PROJECT="$(TEST_PROJECT)" sh scripts/test-smoke.sh

test-down:
	docker compose -p $(TEST_PROJECT) -f docker-compose.test.yml down --remove-orphans

test-faults:
	sh scripts/test-faults.sh

target-config:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target config

target-pull:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target pull

target-verify-images:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target verify-images

target-ownership-status:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target ownership-status

target-activate:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target activate

target-up-core:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target up-core

target-up-writers:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target up-writers

target-up-notifications:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target up-notifications

target-smoke:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target smoke

target-ps:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target ps

target-down:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target down

lease-rehearsal:
	go test -count=1 -run '^TestWriterLeaseGenerationTransferFencesStaleOwner$$' ./internal/ownership

worker-lease-rehearsal:
	go test -count=1 -run '^TestGoWorkerLeaseGroupTransfersAllScopesAndRejectsDuplicate$$' ./internal/ownership

privacy-check:
	go run ./cmd/releasectl privacy --root .

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

run:
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target config
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target pull
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target verify-images
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target activate
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target up-core
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target up-writers
	CPA_RELEASE_ROOT="$(CURDIR)" CPA_ENV_FILE="$(TARGET_ENV)" sh scripts/run.sh __target smoke
