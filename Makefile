VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
GOFLAGS ?=
LDFLAGS := -s -w -X main.version=$(VERSION)
WEB_SOURCES := $(shell find web/src -type f 2>/dev/null) web/index.html web/package.json web/package-lock.json web/vite.config.ts web/tsconfig.json web/tailwind.config.ts

.PHONY: all build web typecheck webtest test race vet verify container-offline clean

all: build

web/node_modules/.install-stamp: web/package-lock.json
	cd web && npm ci
	touch $@

web/dist/index.html: $(WEB_SOURCES) web/node_modules/.install-stamp
	cd web && npm run build

web: web/dist/index.html

build: web
	CGO_ENABLED=0 go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o bin/crproxy ./cmd/crproxy

typecheck: web/node_modules/.install-stamp
	cd web && npm run typecheck

webtest: web/node_modules/.install-stamp
	cd web && npm test

test: web
	go test ./... -count=1

race: web
	go test -race ./... -count=1

vet: web
	go vet ./...

verify: test race vet typecheck webtest
	cd web && npm run build

container-offline: build
	cp /etc/ssl/certs/ca-certificates.crt ca-certificates.crt
	podman build --format docker --ignorefile deploy/container/.containerignore.runtime \
		-f deploy/container/Containerfile.runtime -t crproxy:offline .
	rm ca-certificates.crt

clean:
	rm -rf bin web/dist web/node_modules/.install-stamp
