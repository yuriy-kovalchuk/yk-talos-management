.PHONY: all manifests build test test-cover lint fmt tidy run clean \
        kind-up kind-down kind-deploy kind-install-crds kind-load \
        monitoring-up monitoring-down \
        talos-up talos-down talos-ips talos-clean \
        controller-gen crd-ref-docs api-docs \
        docker-build docker-build-local docker-push buildx-setup \
        install-hooks deps-check \
        help

# ── Variables ─────────────────────────────────────────────────────────────────

BINARY     := bin/manager
CRD_DIR    := config/crd/bases
RBAC_DIR   := config/rbac
KIND_CLUSTER := talos-kind-dev
KIND_CONFIG  := hack/kind-config.yaml

# Local tool cache — keeps project tooling isolated from the system GOPATH.
LOCALBIN        ?= $(shell pwd)/bin
CONTROLLER_GEN  ?= $(LOCALBIN)/controller-gen
CRD_REF_DOCS    ?= $(LOCALBIN)/crd-ref-docs

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

## crd-ref-docs: install crd-ref-docs into the local bin directory
crd-ref-docs: $(LOCALBIN)
	GOBIN=$(LOCALBIN) go install github.com/elastic/crd-ref-docs@latest

## api-docs: generate API reference documentation from CRD types
api-docs: crd-ref-docs
	$(CRD_REF_DOCS) \
	  --source-path=./api/v1alpha1 \
	  --config=hack/crd-ref-docs.yaml \
	  --renderer=markdown \
	  --output-path=docs/api-reference.md

$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## manifests: regenerate CRD and RBAC manifests from kubebuilder markers
manifests: controller-gen
	@echo "Generating CRD manifests..."
	mkdir -p $(CRD_DIR) $(RBAC_DIR)
	$(CONTROLLER_GEN) crd:crdVersions=v1 \
	    paths="./api/..." \
	    output:crd:artifacts:config=$(CRD_DIR)
	$(CONTROLLER_GEN) rbac:roleName=yk-talos-management-manager \
	    paths="./internal/controller/..." \
	    output:rbac:artifacts:config=$(RBAC_DIR)
	@echo "Syncing CRDs to Helm chart..."
	@for crd in $(CRD_DIR)/*.yaml; do \
	    name=$$(basename $$crd); \
	    dst=charts/yk-talos-management/templates/crds/$$name; \
	    { printf '{{- if .Values.crds.install }}\n'; cat $$crd; printf '{{- end }}\n'; } > $$dst; \
	done

# ── Build ─────────────────────────────────────────────────────────────────────

## build: compile the manager binary
build:
	mkdir -p bin
	go build -trimpath -ldflags="$(LDFLAGS) -X $(VERSION_PKG).Version=$(VERSION)" -o $(BINARY) ./cmd/

## run: run the manager locally (webhooks disabled — no TLS outside a cluster)
run: build
	DISABLE_WEBHOOKS=true ./$(BINARY) --zap-encoder=console --zap-log-level=1

## clean: remove build artefacts
clean:
	rm -rf bin/ coverage.out $(CRD_DIR) $(RBAC_DIR)

# ── Docker ────────────────────────────────────────────────────────────────────

## buildx-setup: create or start the multi-platform buildx builder
buildx-setup:
	docker buildx create --name multiplatform --driver docker-container --bootstrap --use 2>/dev/null || \
	  docker buildx inspect --bootstrap multiplatform

## docker-build-local: build image for current platform and load into local Docker daemon (for kind)
docker-build-local:
	docker build \
	  --build-arg VERSION=$(VERSION) \
	  --build-arg COMMIT=$(GIT_COMMIT) \
	  --build-arg BUILD_DATE=$(BUILD_DATE) \
	  -t $(IMAGE):$(VERSION) \
	  -t $(IMAGE):latest \
	  .

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

# ── Talos nodes (ephemeral Docker containers) ─────────────────────────────────

# Talos version must match the machinery version in go.mod.
TALOS_VERSION        ?= v1.13.0
TALOS_DOCKER_NETWORK ?= talos-test
TALOS_NODE_NAME      ?= cp1
TALOS_NODES_SCRIPT   := hack/talos-nodes.sh

TALOS_ENV = \
  TALOS_VERSION=$(TALOS_VERSION) \
  TALOS_DOCKER_NETWORK=$(TALOS_DOCKER_NETWORK) \
  TALOS_NODE_NAME=$(TALOS_NODE_NAME) \
  KIND_CLUSTER=$(KIND_CLUSTER)

## talos-up: start one ephemeral Talos Docker node (TALOS_NODE_NAME=cp1)
talos-up:
	$(TALOS_ENV) $(TALOS_NODES_SCRIPT) up

## talos-down: stop and remove a specific Talos Docker node (TALOS_NODE_NAME=cp1)
talos-down:
	$(TALOS_ENV) $(TALOS_NODES_SCRIPT) down

## talos-ips: print IP addresses of running Talos nodes on the kind network
talos-ips:
	$(TALOS_ENV) $(TALOS_NODES_SCRIPT) ips

## talos-clean: force-remove all containers on the Talos Docker network, then remove the network
talos-clean:
	@docker ps -aq --filter network=$(TALOS_DOCKER_NETWORK) | xargs docker rm -f 2>/dev/null || true
	@docker network rm $(TALOS_DOCKER_NETWORK) 2>/dev/null || true
	@echo "All containers on network '$(TALOS_DOCKER_NETWORK)' removed."

# ── Kind cluster ──────────────────────────────────────────────────────────────

## kind-up: create local Kind cluster (no-op if already exists)
kind-up:
	@kind get clusters 2>/dev/null | grep -q "^$(KIND_CLUSTER)$$" \
	  && echo "Kind cluster '$(KIND_CLUSTER)' already exists, skipping." \
	  || kind create cluster --name $(KIND_CLUSTER) --config $(KIND_CONFIG)

## kind-install-crds: install only CRDs into kind (no operator deployment — use with make run)
kind-install-crds: manifests
	kubectl --context kind-$(KIND_CLUSTER) apply -k config/crd/

## kind-down: delete local Kind cluster
kind-down:
	kind delete cluster --name $(KIND_CLUSTER)

## kind-load: build image for current platform and load it into the kind cluster
kind-load: docker-build-local
	kind load docker-image $(IMAGE):latest --name $(KIND_CLUSTER)
	@echo "Loaded $(IMAGE):latest into kind cluster '$(KIND_CLUSTER)'"

## kind-deploy: create cluster, build+load image, apply manifests, restart operator pod
kind-deploy: manifests kind-up kind-load
	kubectl --context kind-$(KIND_CLUSTER) apply -k config/default/
	kubectl --context kind-$(KIND_CLUSTER) rollout restart deployment/yk-talos-management \
	  -n yk-talos-management-system

# ── Monitoring (local kind) ───────────────────────────────────────────────────

MONITORING_NAMESPACE  ?= monitoring
MONITORING_RELEASE    ?= kube-prometheus-stack
MONITORING_VALUES     ?= hack/monitoring/kube-prometheus-stack-values.yaml

## monitoring-up: install kube-prometheus-stack and apply the ServiceMonitor for the operator
monitoring-up:
	helm repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
	helm repo update prometheus-community
	kubectl get namespace $(MONITORING_NAMESPACE) >/dev/null 2>&1 || kubectl create namespace $(MONITORING_NAMESPACE)
	helm upgrade --install $(MONITORING_RELEASE) prometheus-community/kube-prometheus-stack \
	  --namespace $(MONITORING_NAMESPACE) \
	  --values $(MONITORING_VALUES) \
	  --wait
	kubectl --context kind-$(KIND_CLUSTER) apply -k config/monitoring/

## monitoring-down: uninstall kube-prometheus-stack from the kind cluster
monitoring-down:
	helm uninstall $(MONITORING_RELEASE) --namespace $(MONITORING_NAMESPACE) || true
	kubectl delete namespace $(MONITORING_NAMESPACE) --ignore-not-found

# ── Help ──────────────────────────────────────────────────────────────────────

## help: list available targets
help:
	@grep -E '^## ' Makefile | sed 's/^## //' | column -t -s ':'
