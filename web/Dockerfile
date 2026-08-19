ARG WEB_BASE_IMAGE=docker.m.daocloud.io/library/nginx:1.27.5-alpine@sha256:65645c7bb6a0661892a8b03b89d0743208a18dd2f3f17a54ef4b76fb8e2f2a10
FROM ${WEB_BASE_IMAGE}

COPY web/nginx.conf /etc/nginx/nginx.conf
COPY portal /usr/share/nginx/html/portal
COPY dashboard /usr/share/nginx/html/usage
COPY admin/static /usr/share/nginx/html/admin
RUN nginx -t

LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""

CMD ["nginx", "-g", "daemon off;"]
