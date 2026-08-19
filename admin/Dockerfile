ARG ADMIN_BASE_IMAGE=docker.m.daocloud.io/library/docker:27.5.1-cli@sha256:851f91d241214e7c6db86513b270d58776379aacc5eb9c4a87e5b47115e3065c
FROM ${ADMIN_BASE_IMAGE}

RUN sed -i -e 's|https://dl-cdn.alpinelinux.org/alpine|https://mirrors.aliyun.com/alpine|g' -e 's|http://dl-cdn.alpinelinux.org/alpine|https://mirrors.aliyun.com/alpine|g' /etc/apk/repositories \
    && apk add --no-cache --upgrade expat py3-cryptography python3 tzdata \
    && python3 -c 'import xml.etree.ElementTree as ET; ET.fromstring("<svg/>")'

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    CLIPROXY_APP_ROOT=/opt/codex-cpa-runtime \
    CLIPROXY_ROOT=/var/lib/codex-cpa-cluster \
    CLIPROXY_ADMIN_PORT=8318

WORKDIR /opt/codex-cpa-runtime
COPY admin ./admin
COPY scripts ./scripts
RUN chmod +x ./scripts/cliproxy.py ./scripts/cliproxy.sh \
    && ln -s /opt/codex-cpa-runtime/scripts/cliproxy.sh /usr/local/bin/codex-cpa

LABEL io.codex-cpa.app-root="/opt/codex-cpa-runtime" \
      org.opencontainers.image.source="https://github.com/Alfonsxh/codex-cpa-cluster" \
      org.opencontainers.image.version="" \
      org.opencontainers.image.revision=""

EXPOSE 8318

CMD ["python3", "/opt/codex-cpa-runtime/admin/server.py"]
