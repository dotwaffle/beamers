# syntax=docker/dockerfile:1@sha256:87999aa3d42bdc6bea60565083ee17e86d1f3339802f543c0d03998580f9cb89

ARG BASE_IMAGE=alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
FROM ${BASE_IMAGE}

ARG VERSION
ARG SOURCE_DATE_EPOCH
LABEL org.opencontainers.image.title="Beamers" \
	org.opencontainers.image.version="${VERSION}"

RUN mkdir -p /var/lib/beamers && chown 65532:65532 /var/lib/beamers
COPY --from=beamers-binary --chmod=0555 /beamers-linux-amd64 /usr/local/bin/beamers

USER 65532:65532
WORKDIR /var/lib/beamers
VOLUME ["/var/lib/beamers"]
EXPOSE 8443
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/beamers"]
CMD ["help"]
