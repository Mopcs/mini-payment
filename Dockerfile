FROM golang:1.26.5-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

FROM build AS build-api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/api

FROM build AS build-worker
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/worker

FROM build AS build-consumer
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/consumer

# The binaries are static, so the runtime image needs nothing but the file.
# distroless nonroot also removes the shell and package manager from the image.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app
USER nonroot:nonroot

FROM runtime AS worker
COPY --from=build-worker /out/app /app/service
ENTRYPOINT ["/app/service"]

FROM runtime AS consumer
COPY --from=build-consumer /out/app /app/service
ENTRYPOINT ["/app/service"]

FROM runtime AS api
COPY --from=build-api /out/app /app/service
ENTRYPOINT ["/app/service"]
