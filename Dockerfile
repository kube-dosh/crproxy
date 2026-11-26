# syntax=docker/dockerfile:1.7
ARG NODE_IMAGE=node:24-alpine
ARG GO_IMAGE=golang:1.24-alpine
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM ${NODE_IMAGE} AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM ${GO_IMAGE} AS build
ARG VERSION=0.1.0-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/crproxy ./cmd/crproxy

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/crproxy /crproxy
COPY --chown=65532:65532 deploy/container/crproxy.yaml /etc/crproxy/crproxy.yaml
COPY --chown=65532:65532 deploy/container/data/. /var/lib/crproxy/
USER 65532:65532
EXPOSE 5000
VOLUME ["/var/lib/crproxy"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/crproxy", "healthcheck", "-url", "http://127.0.0.1:5000/api/v1/readyz"]
ENTRYPOINT ["/crproxy", "serve", "-config", "/etc/crproxy/crproxy.yaml"]
