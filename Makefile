
BUILD_DATE?=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ')
GIT_VERSION?=$(shell git describe --tags --dirty --abbrev=0 2>/dev/null || git symbolic-ref --short HEAD)
GIT_COMMIT?=$(shell git rev-parse HEAD 2>/dev/null)
GIT_BRANCH?=$(shell git symbolic-ref --short HEAD 2>/dev/null)
VERSION?=$(shell echo "${GIT_VERSION}" | sed -e 's/^v//')

BIN_DIR?=bin
IMAGE_REGISTRY?=registry.cn-hangzhou.aliyuncs.com
IMAGE_REPOSITORY?=xiaoshiai
IMAGE_NAME?=accelerator-exporter

LDFLAGS+=-w -s
LDFLAGS+=-X 'xiaoshiai.cn/common/version.gitVersion=${GIT_VERSION}'
LDFLAGS+=-X 'xiaoshiai.cn/common/version.gitCommit=${GIT_COMMIT}'
LDFLAGS+=-X 'xiaoshiai.cn/common/version.buildDate=${BUILD_DATE}'
BUILD_TARGET?=./cmd/...
##@ All
all: build ## build all


define build-binary
	@echo "Building ${1}-${2}";
	@mkdir -p ${BIN_DIR}/${1}-${2};
	GOOS=${1} GOARCH=$(2) CGO_ENABLED=0 go build -gcflags=all="-N -l" -ldflags="${LDFLAGS}" -o ${BIN_DIR}/${1}-${2} $(BUILD_TARGET)
endef
##@ Build
.PHONY: build
build: ## Build local binary.
	$(call build-binary,linux,amd64)
	$(call build-binary,linux,arm64)

.PHONY: release
release:release-image release-helm  ## Release all artifacts.

BUILDX_PLATFORMS?=linux/amd64,linux/arm64
release-image:
	docker buildx build --platform=${BUILDX_PLATFORMS} --push -t $(IMAGE_REGISTRY)/$(IMAGE_REPOSITORY)/$(IMAGE_NAME):$(GIT_VERSION) -f Dockerfile ${BIN_DIR}


package-helm: ## Package helm chart.
	@mkdir -p ${BIN_DIR}/charts
	@for dir in $$(find deploy -maxdepth 1 -mindepth 1 -type d); do \
		echo "Packaging $${dir}..."; \
		if [ -f "$${dir}/Chart.yaml" ]; then \
			helm dependency build $${dir}; \
			helm package --version ${VERSION} --app-version ${GIT_VERSION} --destination ${BIN_DIR}/charts $${dir}; \
		else \
			echo "Skipping $${dir} - not a Helm chart"; \
		fi; \
	done

HELM_OCI_REGISTRY?=registry.xiaoshiai.cn/charts
.PHONY: release-helm
release-helm: package-helm## Release helm chart.
	@for chart in $$(find ${BIN_DIR}/charts -name "*.tgz"); do \
		echo "Uploading $${chart}..."; \
		helm push $${chart} oci://${HELM_OCI_REGISTRY}; \
	done

clean:
	rm -rf ${BIN_DIR}
