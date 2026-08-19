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

.PHONY: help verify dev-config dev-build dev-up privacy-check package images publish publish-harbor publish-dockerhub publish-ghcr publish-all release-check release deploy

help:
	@printf '%s\n' \
	  'make verify' \
	  'make dev-build' \
	  'make dev-up' \
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

dev-config:
	docker compose --env-file .env --env-file $(COMPOSE_RUNTIME_ENV) -f docker-compose.yml -f docker-compose.dev.yml config --quiet

dev-build:
	docker compose --env-file .env --env-file $(COMPOSE_RUNTIME_ENV) -f docker-compose.yml -f docker-compose.dev.yml build admin web gateway-blue edge

dev-up:
	docker compose --env-file .env --env-file state/compose.env -f docker-compose.yml -f docker-compose.dev.yml -f compose.accounts.yml up -d

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
