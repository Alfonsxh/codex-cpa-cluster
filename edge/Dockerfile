ARG EDGE_BASE_IMAGE=docker.m.daocloud.io/openresty/openresty:1.31.1.1-2-alpine-fat@sha256:427d94fea0c24b099e7891e8d1b7976f6d008e2d427e56bab725c8b8b293795b
FROM ${EDGE_BASE_IMAGE}

COPY edge/nginx.conf /usr/local/openresty/nginx/conf/nginx.conf
RUN install -d -m 755 /var/run/cliproxy-edge /var/log/cliproxy \
    && printf '%s\n' 'set $active_gateway_backend gateway-blue:8317;' \
      > /var/run/cliproxy-edge/active-gateway.conf \
    && /usr/local/openresty/bin/openresty -t

LABEL org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""

CMD ["/usr/local/openresty/bin/openresty", "-g", "daemon off;"]
