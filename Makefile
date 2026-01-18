# Image URL for the optimizer
IMG_OPTIMIZE ?= ghcr.io/maccam912/karpetrack-optimize:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: fmt vet ## Run tests.
	go test ./... -coverprofile cover.out

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run

##@ Build

.PHONY: build
build: build-optimize ## Build all binaries.

.PHONY: build-optimize
build-optimize: fmt vet ## Build optimize CLI binary.
	go build -o bin/karpetrack-optimize ./cmd/optimize

.PHONY: build-discover-minbids
build-discover-minbids: fmt vet ## Build discover-minbids CLI binary.
	go build -o bin/discover-minbids ./cmd/discover-minbids

##@ Docker

.PHONY: docker-build
docker-build: ## Build docker image for the optimizer.
	docker build -f Dockerfile.optimize -t ${IMG_OPTIMIZE} .

.PHONY: docker-push
docker-push: ## Push docker image for the optimizer.
	docker push ${IMG_OPTIMIZE}
