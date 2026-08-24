ARG GO_BUILDER_IMAGE=docker.m.daocloud.io/library/golang:1.25.0-alpine3.22@sha256:f18a072054848d87a8077455f0ac8a25886f2397f88bfdd222d6fafbb5bba440
ARG NODE_BUILDER_IMAGE=docker.m.daocloud.io/library/node:22-alpine3.22@sha256:cd7807368cf24826297cbad5dca1a44972ccfd770647db52a8c7589eb4599ac8
ARG RUNTIME_IMAGE=docker.m.daocloud.io/library/alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

FROM ${GO_BUILDER_IMAGE} AS go-builder
ARG GOPROXY=https://goproxy.cn
ARG GOSUMDB=sum.golang.google.cn
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN mkdir -p /out \
    && for command in admin collector docker-read-proxy edge failover gateway log-maintenance migration-compare notifications ownership quota test-upstream web; do \
         go build -tags timetzdata -trimpath -buildvcs=false -ldflags='-s -w' -o "/out/cpa-${command}" "./cmd/${command}"; \
       done

FROM ${NODE_BUILDER_IMAGE} AS web-builder
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --registry=https://registry.npmmirror.com
COPY frontend ./
RUN npm run build

FROM ${RUNTIME_IMAGE} AS go-runtime
RUN test -s /etc/ssl/certs/ca-certificates.crt \
    && addgroup -S -g 10001 cpa \
    && adduser -S -D -H -u 10001 -G cpa cpa
LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""

FROM go-runtime AS gateway
COPY --from=go-builder /out/cpa-gateway /usr/local/bin/cpa-gateway
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-gateway"]

FROM go-runtime AS edge
COPY --from=go-builder /out/cpa-edge /usr/local/bin/cpa-edge
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-edge"]

FROM go-runtime AS control
COPY --from=go-builder /out/cpa-admin /out/cpa-collector /out/cpa-docker-read-proxy /out/cpa-failover \
  /out/cpa-log-maintenance /out/cpa-migration-compare /out/cpa-notifications \
  /out/cpa-ownership /out/cpa-quota \
  /usr/local/bin/
CMD ["/usr/local/bin/cpa-admin"]

FROM go-runtime AS test-upstream
COPY --from=go-builder /out/cpa-test-upstream /usr/local/bin/cpa-test-upstream
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-test-upstream"]

FROM go-runtime AS web
COPY --from=go-builder /out/cpa-web /usr/local/bin/cpa-web
COPY --from=web-builder /src/frontend/dist/portal /srv/cpa-web/portal
COPY --from=web-builder /src/frontend/dist/admin /srv/cpa-web/admin
COPY --from=web-builder /src/frontend/dist/usage /srv/cpa-web/usage
LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""
USER cpa:cpa
ENTRYPOINT ["/usr/local/bin/cpa-web"]
