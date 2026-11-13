# Test-only derivative of the image built by the repository's root Dockerfile.
ARG PRODUCT_IMAGE
FROM ${PRODUCT_IMAGE}
RUN apt-get update \
    && apt-get install -y --no-install-recommends sqlite3 socat \
    && rm -rf /var/lib/apt/lists/*
COPY deploy/d1/entrypoint.sh /d1-entrypoint.sh
RUN chmod 0555 /d1-entrypoint.sh
ENTRYPOINT ["/d1-entrypoint.sh"]
