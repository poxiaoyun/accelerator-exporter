SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

BUILD_DATE ?= $(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
override BUILD_DATE := $(BUILD_DATE)
GIT_VERSION ?= $(shell git describe --tags --dirty 2>/dev/null || git symbolic-ref --short HEAD 2>/dev/null || echo dev)
override GIT_VERSION := $(subst /,-,$(GIT_VERSION))
GIT_COMMIT ?= $(shell commit=$$(git rev-parse HEAD 2>/dev/null || true); if [ -n "$${commit}" ] && [ "$${commit}" != HEAD ]; then echo "$${commit}"; else echo unknown; fi)
override GIT_COMMIT := $(GIT_COMMIT)
VERSION ?= $(shell version=$$(echo "${GIT_VERSION}" | sed -nE 's/^v?([0-9]+\.[0-9]+\.[0-9]+.*)$$/\1/p'); if [ -n "$${version}" ]; then echo "$${version}"; else echo 0.0.0; fi)
override VERSION := $(VERSION)

BIN_DIR ?= bin
BUILD_TARGET ?= ./cmd/accelerator-exporter

IMAGE_REGISTRY ?= registry.cn-hangzhou.aliyuncs.com
IMAGE_REPOSITORY ?= xiaoshiai
IMAGE_NAME ?= accelerator-exporter
IMAGE ?= $(IMAGE_REGISTRY)/$(IMAGE_REPOSITORY)/$(IMAGE_NAME):$(GIT_VERSION)
BUILDX_PLATFORMS ?= linux/amd64,linux/arm64
BUILDX_CACHE_TYPE ?=

REGISTRY_USERNAME ?=
REGISTRY_PASSWORD ?=
HELM_REGISTRY ?= registry.xiaoshiai.cn
HELM_OCI_REGISTRY ?= $(HELM_REGISTRY)/charts

LDFLAGS ?= -w -s

comma := ,
define buildx-cache-args
$(if $(strip ${BUILDX_CACHE_TYPE}),--cache-from=type=${BUILDX_CACHE_TYPE}$(comma)scope=$(1) --cache-to=type=${BUILDX_CACHE_TYPE}$(comma)scope=$(1)$(comma)mode=max$(comma)ignore-error=true)
endef

define build-binary
	@echo "Building ${1}-${2}"
	@mkdir -p "${BIN_DIR}/${1}-${2}"
	GOOS=${1} GOARCH=${2} CGO_ENABLED=0 go build -ldflags="${LDFLAGS}" -o "${BIN_DIR}/${1}-${2}/accelerator-exporter" ${BUILD_TARGET}
endef

##@ General
.PHONY: all help
all: build ## Build all binaries.

help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development
.PHONY: fmt test check verify
fmt: ## Format Go source files.
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

test: ## Run unit tests with coverage.
	go test ./... -coverprofile=coverage.out -covermode=atomic

check: ## Run formatting, vet, and unit-test checks.
	test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"
	go vet ./...
	go test ./...

verify: check helm-lint build ## Run all repository verification.

##@ Build
.PHONY: build
build: ## Build linux/amd64 and linux/arm64 binaries.
	$(call build-binary,linux,amd64)
	$(call build-binary,linux,arm64)

##@ Container
.PHONY: docker-build release-image
docker-build: build ## Build the local linux/amd64 container image.
	docker build \
		--build-arg TARGETOS=linux \
		--build-arg TARGETARCH=amd64 \
		-t "${IMAGE}" \
		-f Dockerfile \
		"${BIN_DIR}"

release-image: build ## Build and push the multi-platform container image.
	docker buildx build --platform="${BUILDX_PLATFORMS}" --push \
		--provenance=false \
		$(call buildx-cache-args,accelerator-exporter-image) \
		-t "${IMAGE}" \
		-f Dockerfile \
		"${BIN_DIR}"

##@ Release
.PHONY: release
release: release-image release-helm ## Release the image and Helm chart.

##@ Helm
.PHONY: helm-lint package-helm release-helm
helm-lint: ## Lint the accelerator-exporter Helm chart.
	helm dependency build --skip-refresh deploy/accelerator-exporter
	helm lint deploy/accelerator-exporter

package-helm: ## Package the accelerator-exporter Helm chart.
	@mkdir -p "${BIN_DIR}/charts"
	helm dependency build --skip-refresh deploy/accelerator-exporter
	helm package deploy/accelerator-exporter --version="${VERSION}" --app-version="${GIT_VERSION}" --destination "${BIN_DIR}/charts"

release-helm: package-helm ## Push the accelerator-exporter Helm chart.
	helm push "${BIN_DIR}/charts/accelerator-exporter-${VERSION}.tgz" "oci://${HELM_OCI_REGISTRY}"

##@ Registry
.PHONY: login login-docker login-helm
login: login-docker login-helm ## Log in to the image and Helm registries.

login-docker:
	@printf '%s' "$${REGISTRY_PASSWORD}" | docker login "${IMAGE_REGISTRY}" --username "$${REGISTRY_USERNAME}" --password-stdin

login-helm:
	@printf '%s' "$${REGISTRY_PASSWORD}" | helm registry login "${HELM_REGISTRY}" --username "$${REGISTRY_USERNAME}" --password-stdin

##@ Cleanup
.PHONY: clean
clean: ## Remove generated build output.
	rm -rf "${BIN_DIR}" coverage.out
