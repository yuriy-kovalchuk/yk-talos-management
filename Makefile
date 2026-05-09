.PHONY: all manifests build test test-cover lint fmt tidy run clean \
        kind-up kind-down kind-deploy \
        controller-gen \
        docker-build docker-push buildx-setup \
        install-hooks deps-check \
        help

# ── Variables ─────────────────────────────────────────────────────────────────

BINARY     := bin/manager
CRD_DIR    := config/crd/bases
RBAC_DIR   := config/rbac
KIND_CLUSTER := talos-kind-dev
KIND_CONFIG  := hack/kind-config.yaml

# Local tool cache — keeps project tooling isolated from the system GOPATH.
LOCALBIN       ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

VERSION_PKG := github.com/yuriy-kovalchuk/yk-talos-management/internal/version
GIT_COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE  := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w \
  -X $(VERSION_PKG).Commit=$(GIT_COMMIT) \
  -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

# ── Image ─────────────────────────────────────────────────────────────────────
# Override IMAGE and VERSION on the command line:
#   make docker-build IMAGE=localhost:5000/yk-talos-management VERSION=dev
#   make docker-push  IMAGE=ghcr.io/yuriy-kovalchuk/yk-talos-management VERSION=v0.1.0
IMAGE     ?= ghcr.io/yuriy-kovalchuk/yk-talos-management
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
PLATFORMS ?= linux/amd64,linux/arm64

# ── Default ───────────────────────────────────────────────────────────────────

all: tidy fmt lint manifests build

# ── Code quality ──────────────────────────────────────────────────────────────

## fmt: format all Go source files
fmt:
	go fmt ./...

## lint: run go vet
lint:
	go vet ./...

## tidy: tidy go modules
tidy:
	go mod tidy

## deps-check: list direct dependencies that have newer versions available
deps-check:
	@go list -u -m -f '{{if and (not .Indirect) .Update}}{{.Path}}  {{.Version}} → {{.Update.Version}}{{end}}' all | grep -v "^$$" || echo "All direct dependencies are up to date."

## test: run all tests
test:
	go test -race -timeout 120s ./...

## install-hooks: configure git to use the tracked hooks in .githooks/
install-hooks:
	git config core.hooksPath .githooks
	@echo "Git hooks installed — tests will run before every push."

## test-cover: run tests with coverage report
test-cover:
	go test -race -timeout 120s -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

# ── Code generation ───────────────────────────────────────────────────────────

## controller-gen: install controller-gen into the local bin directory
controller-gen: $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## manifests: regenerate CRD and RBAC manifests from kubebuilder markers
manifests: controller-gen
	@echo "Generating CRD manifests..."
	mkdir -p $(CRD_DIR) $(RBAC_DIR)
	$(CONTROLLER_GEN) crd:crdVersions=v1 \
	    paths="./api/..." \
	    output:crd:artifacts:config=$(CRD_DIR)
	$(CONTROLLER_GEN) rbac:roleName=manager \
	    paths="./internal/controller/..." \
	    output:rbac:artifacts:config=$(RBAC_DIR)

# ── Build ─────────────────────────────────────────────────────────────────────

## build: compile the manager binary
build:
	mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS) -X $(VERSION_PKG).Version=$(VERSION)" -o $(BINARY) ./cmd/

## run: run the manager locally (webhooks disabled — no TLS outside a cluster)
run: build
	DISABLE_WEBHOOKS=true ./$(BINARY)

## clean: remove build artefacts
clean:
	rm -rf bin/ coverage.out $(CRD_DIR) $(RBAC_DIR)

# ── Docker ────────────────────────────────────────────────────────────────────

## buildx-setup: create or start the multi-platform buildx builder
buildx-setup:
	docker buildx create --name multiplatform --driver docker-container --bootstrap --use 2>/dev/null || \
	  docker buildx inspect --bootstrap multiplatform

## docker-build: build multi-arch image (does not push)
docker-build: buildx-setup
	docker buildx build \
	  --builder multiplatform \
	  --platform $(PLATFORMS) \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(GIT_COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) \
	  -t $(IMAGE):latest \
	  .

## docker-push: build and push multi-arch image
docker-push: buildx-setup
	docker buildx build \
	  --builder multiplatform \
	  --platform $(PLATFORMS) \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(GIT_COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) \
	  -t $(IMAGE):latest \
	  --push \
	  .

# ── Kind cluster ──────────────────────────────────────────────────────────────

## kind-up: create local Kind cluster
kind-up:
	kind create cluster --name $(KIND_CLUSTER) --config $(KIND_CONFIG)

## kind-down: delete local Kind cluster
kind-down:
	kind delete cluster --name $(KIND_CLUSTER)

## kind-deploy: apply CRDs and RBAC to the current cluster
kind-deploy: manifests
	kubectl apply -f $(CRD_DIR)/
	kubectl apply -f $(RBAC_DIR)/

# ── Help ──────────────────────────────────────────────────────────────────────

## help: list available targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //' | column -t -s ':'
