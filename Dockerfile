# Nieuwsdashboard: static Go binary on distroless (no shell, no package manager), non-root.
# Build:  docker build -t nieuwsdashboard .
# Run:    see docker-compose.yml.default (copy it to docker-compose.yml)

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go feeds.go panels.go main_test.go config.yaml.default VERSION ./
COPY web ./web
# the image is only built when vet and the unit tests pass (including a check of config.yaml.default)
RUN go vet ./... && go test -count=1 ./...
# the version is read from the VERSION file, so a local docker-compose.yml never goes stale
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(tr -d ' \n' < VERSION)" -o /out/nieuwsdashboard .

# distroless/static ships CA certificates and tzdata (the binary embeds tzdata as well).
FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=build /out/nieuwsdashboard /app/nieuwsdashboard
# built-in default; docker-compose.yml mounts your own config.yaml over it
COPY config.yaml.default /app/config.yaml
ENV NDB_LISTEN=0.0.0.0:8080 \
    NDB_CONFIG=/app/config.yaml \
    GOMEMLIMIT=96MiB
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/app/nieuwsdashboard", "-healthcheck"]
ENTRYPOINT ["/app/nieuwsdashboard"]
