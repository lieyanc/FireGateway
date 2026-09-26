.PHONY: all web build run dev dev-web test lint clean

BIN     ?= bin/firegateway
CONFIG  ?= config.json
PKG     := github.com/lieyanc/FireGateway/internal/version
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X $(PKG).Version=$(VERSION) -X $(PKG).Commit=$(COMMIT) -X $(PKG).BuildTime=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)

all: build

web/node_modules: web/package-lock.json
	npm ci --prefix web
	@touch $@

# Build the UI into web/dist (embedded by the Go binary).
web: web/node_modules
	npm run --prefix web build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/firegateway

# Full build (UI + binary), then run it.
run: build
	./$(BIN) -c $(CONFIG)

# Backend with the UI currently in web/dist; run `make dev-web` alongside for hot reload.
dev:
	go run ./cmd/firegateway -c $(CONFIG)

dev-web: web/node_modules
	npm run --prefix web dev

test:
	go vet ./...
	go test -race ./...

lint: web/node_modules
	npm run --prefix web lint

clean:
	rm -rf bin web/dist/assets web/dist/*.html
