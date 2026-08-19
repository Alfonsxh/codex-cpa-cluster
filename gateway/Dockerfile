ARG GATEWAY_BASE_IMAGE=docker.m.daocloud.io/openresty/openresty:1.31.1.1-2-alpine-fat@sha256:427d94fea0c24b099e7891e8d1b7976f6d008e2d427e56bab725c8b8b293795b
FROM ${GATEWAY_BASE_IMAGE}

COPY gateway/nginx.conf /usr/local/openresty/nginx/conf/nginx.conf
COPY gateway/gateway_state.lua /usr/local/openresty/nginx/conf/custom/gateway_state.lua
COPY gateway/request_gate.lua /usr/local/openresty/nginx/conf/custom/request_gate.lua
RUN install -d -m 755 /var/log/cliproxy \
    && /usr/local/openresty/bin/openresty -t

LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""

CMD ["/usr/local/openresty/bin/openresty", "-g", "daemon off;"]
