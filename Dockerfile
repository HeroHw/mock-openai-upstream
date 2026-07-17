# syntax=docker/dockerfile:1

# ── build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS build
WORKDIR /src

# Module files first for layer caching. There are no third-party deps (stdlib
# only), so this stays trivial, but it keeps the pattern conventional.
COPY go.mod ./
RUN go mod download

COPY . .

# Static, stripped binary so it runs in a scratch image.
# CGO off → no libc dependency; -w -s strips debug info to shrink the binary.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-w -s" \
    -o /out/mockupstream ./cmd/mockupstream

# ── runtime stage ────────────────────────────────────────────────────────────
FROM scratch AS runtime

# Bundle a non-root user and CA certs from the build image. The mock makes no
# outbound TLS calls today, but certs cost nothing and keep the image generic.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/mockupstream /mockupstream

# The listen port is fixed at :9050 inside the container. To change the
# externally-published port, edit the docker-compose port mapping (or `docker
# run -p`), not anything here.
EXPOSE 9050

# Run as an unprivileged uid (scratch has no /etc/passwd; numeric uid is fine).
USER 65532:65532

# The scratch image has no shell or curl, so the binary self-probes via its
# -healthcheck mode (exits 0 healthy / 1 unhealthy).
HEALTHCHECK --interval=10s --timeout=3s --start-period=2s --retries=3 \
    CMD ["/mockupstream", "-healthcheck"]

ENTRYPOINT ["/mockupstream"]
