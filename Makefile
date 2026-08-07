# CFTM — developer Makefile
#
# Common tasks for building, generating assets and running locally.

BINARY      := bin/cftm
PKG         := ./...
TEMPL       := templ
TEMPL_VERSION := v0.3.1020
TAILWIND    := ./bin/tailwindcss
TAILWIND_VERSION := v3.4.17
CSS_INPUT   := internal/web/assets/input.css
CSS_OUTPUT  := internal/web/assets/static/app.css

VERSION ?= $(shell date -u +v%Y%m%d-%H%M)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X github.com/daknoblo/CFTM/internal/version.Version=$(VERSION) \
	-X github.com/daknoblo/CFTM/internal/version.Commit=$(COMMIT) \
	-X github.com/daknoblo/CFTM/internal/version.Date=$(DATE)

.PHONY: all generate css build run test vet lint tidy tools tailwind clean docker

all: generate css build

## Install the templ CLI (run once).
tools:
	go install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)

## Download the standalone Tailwind CLI for this platform (run once).
tailwind:
	@mkdir -p bin
	@os=$$(uname -s | tr '[:upper:]' '[:lower:]'); \
	arch=$$(uname -m); \
	case "$$os-$$arch" in \
	  darwin-arm64) asset=tailwindcss-macos-arm64 ;; \
	  darwin-x86_64) asset=tailwindcss-macos-x64 ;; \
	  linux-aarch64|linux-arm64) asset=tailwindcss-linux-arm64 ;; \
	  linux-x86_64) asset=tailwindcss-linux-x64 ;; \
	  *) echo "unsupported platform $$os-$$arch"; exit 1 ;; \
	esac; \
	curl -fsSL "https://github.com/tailwindlabs/tailwindcss/releases/download/$(TAILWIND_VERSION)/$$asset" -o $(TAILWIND); \
	chmod +x $(TAILWIND)

## Generate Go code from .templ files.
generate:
	$(TEMPL) generate

## Compile the Tailwind CSS into the embedded static output.
css:
	$(TAILWIND) -c tailwind.config.js -i $(CSS_INPUT) -o $(CSS_OUTPUT) --minify

## Build the static binary.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/cftm

## Run locally (requires CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID).
run: build
	$(BINARY)

## Run tests.
test:
	go test -race $(PKG)

## Static analysis.
vet:
	go vet $(PKG)

## Full linter suite.
lint:
	golangci-lint run

## Tidy modules.
tidy:
	go mod tidy

## Build the Docker image.
docker:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t cftm:$(VERSION) .

clean:
	rm -rf bin out
