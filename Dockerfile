FROM docker.io/library/alpine:3.22
# TARGETOS TARGETARCH already set by '--platform'
ARG TARGETOS TARGETARCH
COPY ${TARGETOS}-${TARGETARCH}/accelerator-exporter /usr/local/bin/
WORKDIR /app
ENTRYPOINT ["/usr/local/bin/accelerator-exporter"]
