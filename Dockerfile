# syntax=docker/dockerfile:1.7
#
# Every target compiles only the binary it ships, so building the worker image
# does not pay for the API. The last stage is the default target: the API.

FROM golang:1.26.5-alpine AS build
WORKDIR /src

# Modules first: a source-only change reuses this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -trimpath drops local paths, -buildvcs=false keeps the binary independent of
# checkout metadata, and -s -w strips the symbol table and DWARF data.
ENV CGO_ENABLED=0

FROM build AS build-api
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/app ./cmd/api

FROM build AS build-worker
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/app ./cmd/worker

FROM build AS build-consumer
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/app ./cmd/consumer

FROM build AS build-healthcheck
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/app ./cmd/healthcheck

FROM build AS build-migrate
RUN go build -trimpath -buildvcs=false -ldflags="-s -w" -o /out/app ./cmd/migrate

# distroless static carries the CA bundle the TLS clients need and nothing
# else: no shell, no package manager, no libc. The image already defines the
# nonroot user with uid 65532.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app
USER nonroot:nonroot

FROM runtime AS migrate
COPY --from=build-migrate /out/app /app/migrate
# The migrator resolves file://migrations against the working directory, so the
# SQL files ship next to it.
COPY --from=build /src/migrations /app/migrations
ENTRYPOINT ["/app/migrate"]

# The worker and the consumer open no HTTP API, so the probe targets the metrics
# listener. Answering there proves the process is up and not wedged.
FROM runtime AS worker
COPY --from=build-worker /out/app /app/service
COPY --from=build-healthcheck /out/app /app/healthcheck
HEALTHCHECK --interval=15s --timeout=5s --start-period=15s --retries=4 \
	CMD ["/app/healthcheck", "-url", "http://127.0.0.1:9092/metrics"]
ENTRYPOINT ["/app/service"]

FROM runtime AS consumer
COPY --from=build-consumer /out/app /app/service
COPY --from=build-healthcheck /out/app /app/healthcheck
HEALTHCHECK --interval=15s --timeout=5s --start-period=15s --retries=4 \
	CMD ["/app/healthcheck", "-url", "http://127.0.0.1:9093/metrics"]
ENTRYPOINT ["/app/service"]

FROM runtime AS api
COPY --from=build-api /out/app /app/service
# The probe is a static binary because the image has no shell to run curl.
COPY --from=build-healthcheck /out/app /app/healthcheck
# /ready checks PostgreSQL and Redis, so it covers dependencies, not just the
# process. Use /health for liveness: a dependency outage must not restart the
# API.
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=4 \
	CMD ["/app/healthcheck", "-url", "http://127.0.0.1:8080/ready"]
ENTRYPOINT ["/app/service"]
