ARG GO_BUILDER_IMAGE=docker.m.daocloud.io/library/golang:1.25.0-alpine3.22@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440
ARG NODE_BUILDER_IMAGE=docker.m.daocloud.io/library/node:22-alpine3.22@sha256:cd7807368cf24826297cbad5dca1a44972ccfd770647db52a8c7589eb4599ac8
ARG RUNTIME_IMAGE=docker.m.daocloud.io/library/alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

FROM --platform=$BUILDPLATFORM ${GO_BUILDER_IMAGE} AS go-base
ARG GOPROXY=https://goproxy.cn
ARG GOSUMDB=sum.golang.google.cn
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,id=cpac-go-mod,target=/go/pkg/mod \
    go mod download

FROM go-base AS control-builder
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,id=cpac-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpac-go-build,target=/root/.cache/go-build \
    mkdir -p /out \
    && for command in admin bootstrap collector failover log-maintenance notifications ownership quota releasectl; do \
         go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o "/out/cpa-${command}" "./cmd/${command}"; \
       done

FROM go-base AS gateway-builder
COPY cmd/gateway ./cmd/gateway
COPY internal/gateway ./internal/gateway
RUN --mount=type=cache,id=cpac-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpac-go-build,target=/root/.cache/go-build \
    go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o /out/cpa-gateway ./cmd/gateway

FROM go-base AS edge-builder
COPY cmd/edge ./cmd/edge
COPY internal/edge ./internal/edge
RUN --mount=type=cache,id=cpac-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpac-go-build,target=/root/.cache/go-build \
    go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o /out/cpa-edge ./cmd/edge

FROM go-base AS web-go-builder
COPY cmd/web ./cmd/web
COPY internal/web ./internal/web
RUN --mount=type=cache,id=cpac-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpac-go-build,target=/root/.cache/go-build \
    go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o /out/cpa-web ./cmd/web

FROM go-base AS test-upstream-builder
COPY cmd/test-upstream ./cmd/test-upstream
RUN --mount=type=cache,id=cpac-go-mod,target=/go/pkg/mod \
    --mount=type=cache,id=cpac-go-build,target=/root/.cache/go-build \
    go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o /out/cpa-test-upstream ./cmd/test-upstream

FROM --platform=$BUILDPLATFORM ${NODE_BUILDER_IMAGE} AS web-builder
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,id=cpac-npm-cache,target=/root/.npm \
    npm ci --registry=https://registry.npmmirror.com
COPY frontend ./
RUN --mount=type=cache,id=cpac-npm-cache,target=/root/.npm \
    npm run build

FROM ${RUNTIME_IMAGE} AS go-runtime
# Go embeds tzdata, but libc and shell tools also need the system zoneinfo.
ENV TZ=Asia/Shanghai
RUN sed -i 's|dl-cdn.alpinelinux.org|mirrors.tuna.tsinghua.edu.cn|g' /etc/apk/repositories \
    && apk add --no-cache tzdata \
    && ln -snf /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && test -s /etc/ssl/certs/ca-certificates.crt \
    && addgroup -S -g 10001 cpa \
    && adduser -S -D -H -u 10001 -G cpa cpa
LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""

FROM go-runtime AS gateway
COPY --from=gateway-builder /out/cpa-gateway /usr/local/bin/cpa-gateway
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-gateway"]

FROM go-runtime AS edge
COPY --from=edge-builder /out/cpa-edge /usr/local/bin/cpa-edge
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-edge"]

FROM go-runtime AS control
COPY --from=control-builder /out/cpa-admin /out/cpa-bootstrap /out/cpa-collector /out/cpa-failover \
  /out/cpa-log-maintenance /out/cpa-notifications \
  /out/cpa-ownership /out/cpa-quota /out/cpa-releasectl \
  /usr/local/bin/
CMD ["/usr/local/bin/cpa-admin"]

FROM go-runtime AS test-upstream
COPY --from=test-upstream-builder /out/cpa-test-upstream /usr/local/bin/cpa-test-upstream
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-test-upstream"]

FROM go-runtime AS web
COPY --from=web-go-builder /out/cpa-web /usr/local/bin/cpa-web
COPY --from=web-builder /src/frontend/dist/portal /srv/cpa-web/portal
COPY --from=web-builder /src/frontend/dist/admin /srv/cpa-web/admin
COPY --from=web-builder /src/frontend/dist/usage /srv/cpa-web/usage
LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-web"]
