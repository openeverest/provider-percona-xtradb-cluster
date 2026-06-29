## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

RELEASE_VERSION ?= v0.0.0-$(shell git rev-parse --short HEAD)
RELEASE_FULLCOMMIT ?= $(shell git rev-parse HEAD)

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# OpenEverest branch to use for OpenEverest CRD installation.
OPENEVEREST_BRANCH ?= release-2.0

# Image URL to use all building/pushing image targets
IMG ?= ghcr.io/openeverest/provider-percona-xtradb-cluster-dev:latest

# Image URL for OpenEverest controller used in integration tests (must be pre-built).
OPENEVEREST_CONTROLLER_IMG ?= ghcr.io/openeverest/openeverest-controller-dev:0.0.0

# Split IMG into repository and tag for Helm values
_IMG_REPO = $(firstword $(subst :, ,$(IMG)))
_IMG_TAG  = $(lastword $(subst :, ,$(IMG)))

# controller-gen version
CONTROLLER_TOOLS_VERSION ?= v0.18.0
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)

# yq version for YAML processing
YQ_VERSION ?= v4.44.6
YQ ?= $(LOCALBIN)/yq-$(YQ_VERSION)

# Helm chart directory
CHART_DIR ?= charts/provider-percona-xtradb-cluster

# k3d cluster name (must match dev/k3d_config.yaml metadata.name)
K3D_CLUSTER_NAME ?= provider-pxc-test

# PXC operator version used for CRD installation in CI
PXC ?= 1.20.0

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: lint
lint: ## Run golangci-lint.
	go tool golangci-lint run ./...

.PHONY: run
run: generate ## Run the provider locally.
	go run cmd/provider/main.go

##@ Code Generation

.PHONY: manifests
manifests: controller-gen ## Generate RBAC manifests using controller-gen from kubebuilder markers.
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="./..." output:rbac:dir=config/rbac

.PHONY: helm-sync-rbac
helm-sync-rbac: yq ## Sync generated RBAC rules into the Helm chart.
	@echo "Syncing RBAC rules from config/rbac/role.yaml to Helm chart..."
	@$(YQ) '.rules' config/rbac/role.yaml > $(CHART_DIR)/generated/rbac-rules.yaml
	@echo "Done."

.PHONY: generate
generate: manifests helm-sync-rbac ## Run all code generation (RBAC + Helm sync + provider spec from definition/).
	go generate ./...
	@echo "All generation complete."

.PHONY: verify
verify: ## Verify that generated files are up-to-date (for CI).
	@$(MAKE) generate
	@if git diff --quiet -- config/ $(CHART_DIR)/generated/; then \
		echo "Generated files are up-to-date."; \
	else \
		echo "ERROR: Generated files are out of date. Run 'make generate' and commit the changes."; \
		git diff -- config/ $(CHART_DIR)/generated/; \
		exit 1; \
	fi

##@ Testing

.PHONY: test-unit
test-unit: ## Run Go unit tests.
	go test -v -race -coverprofile=coverage.out ./...

.PHONY: test-integration
test-integration: ## Run all integration tests against K8S cluster.
	. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl.yaml

.PHONY: test-integration-core
test-integration-core: ## Run core integration tests.
	. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl-core.yaml

.PHONY: test-integration-monitoring-pmm
test-integration-monitoring-pmm: ## Run PMM integration tests.
	. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl-monitoring.yaml

.PHONY: test-integration-backup
test-integration-backup: ## Run backup integration tests.
	. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl-backup.yaml

.PHONY: test-integration-backup-datasource
test-integration-backup-datasource: ## Run backup datasource integration tests.
	. ./test/vars.sh && kubectl kuttl test --config ./test/integration/kuttl-backup.yaml --test "datasource"

.PHONY: test-e2e
test-e2e: ## Run Playwright E2E tests against a running Everest UI (http://localhost:8080).
	cd test/e2e && npm ci && npx playwright test

.PHONY: load-image
load-image: ## Import the provider image (IMG) into the k3d cluster.
	k3d image import ${IMG} -c ${K3D_CLUSTER_NAME}

.PHONY: load-openeverest-controller-image
load-openeverest-controller-image: ## Import the OpenEverest controller image into the k3d cluster.
	k3d image import ${OPENEVEREST_CONTROLLER_IMG} -c ${K3D_CLUSTER_NAME}

.PHONY: install-crds
install-crds: ## Install OpenEverest and PXC CRDs into the cluster.
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/$(OPENEVEREST_BRANCH)/config/crd/bases/core.openeverest.io_providers.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/$(OPENEVEREST_BRANCH)/config/crd/bases/core.openeverest.io_instances.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/$(OPENEVEREST_BRANCH)/config/crd/bases/monitoring.openeverest.io_monitoringconfigs.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/$(OPENEVEREST_BRANCH)/config/crd/bases/backup.openeverest.io_backupclasses.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/$(OPENEVEREST_BRANCH)/config/crd/bases/backup.openeverest.io_backups.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/$(OPENEVEREST_BRANCH)/config/crd/bases/backup.openeverest.io_restores.yaml
	kubectl apply -f https://raw.githubusercontent.com/openeverest/openeverest/$(OPENEVEREST_BRANCH)/config/crd/bases/backup.openeverest.io_backupstorages.yaml
	curl -fsSL https://raw.githubusercontent.com/percona/percona-xtradb-cluster-operator/v$(PXC_OPERATOR_VERSION)/deploy/crd.yaml \
		| kubectl apply --server-side -f -

.PHONY: deploy-provider-ci
deploy-provider-ci: ## Deploy the provider via Helm for CI (IMG must already be imported into k3d).
	helm repo add percona https://percona.github.io/percona-helm-charts/
	helm dependency build $(CHART_DIR)
	helm upgrade --install provider-percona-xtradb-cluster $(CHART_DIR) \
		--create-namespace \
		--namespace provider-system \
		--set image.repository=$(_IMG_REPO) \
		--set image.tag=$(_IMG_TAG) \
		--set image.pullPolicy=Never \
		--set operator.replicaCount=0 \
		--wait --timeout 2m

##@ Helm

.PHONY: helm-install
helm-install: ## Install the provider using Helm.
	helm install provider-percona-xtradb-cluster $(CHART_DIR) --create-namespace

.PHONY: helm-upgrade
helm-upgrade: ## Upgrade the provider using Helm.
	helm upgrade provider-percona-xtradb-cluster $(CHART_DIR)

.PHONY: helm-uninstall
helm-uninstall: ## Uninstall the provider using Helm.
	helm uninstall provider-percona-xtradb-cluster

.PHONY: helm-template
helm-template: ## Render Helm chart templates locally (dry-run).
	helm template provider-percona-xtradb-cluster $(CHART_DIR)

##@ Local Development Cluster

.PHONY: k3d-cluster-up
k3d-cluster-up: ## Create a K8S cluster for testing.
	$(info Creating K3D cluster for testing)
	k3d cluster create --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-down
k3d-cluster-down: ## Delete the K8S test cluster.
	$(info Destroying K3D test cluster)
	k3d cluster delete --config ./dev/k3d_config.yaml

.PHONY: k3d-cluster-reset
k3d-cluster-reset: k3d-cluster-down k3d-cluster-up ## Reset the K8S cluster for testing.

##@ Build

LD_FLAGS = -X 'github.com/openeverest/provider-percona-xtradb-cluster/cmd/provider.Version=$(RELEASE_VERSION)' \
	-X 'github.com/openeverest/provider-percona-xtradb-cluster/cmd/provider.FullCommit=$(RELEASE_FULLCOMMIT)'

.PHONY: build-helper
build-helper: generate $(LOCALBIN) ## Build provider binary (helper).
	go build -v -ldflags "$(LD_FLAGS)" -o bin/provider cmd/provider/main.go

.PHONY: build
build: LD_FLAGS += -s -w
build: build-helper ## Build provider binary.

.PHONY: rc
rc: build-helper ## Build provider RC binary.

.PHONY: release
release: LD_FLAGS += -s -w
release: build-helper ## Build provider release binary. (Use for building release only!)

.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image.
	$(CONTAINER_TOOL) push ${IMG}

##@ Tool Dependencies

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Install controller-gen.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: yq
yq: $(YQ) ## Install yq.
$(YQ): $(LOCALBIN)
	@echo "Installing yq $(YQ_VERSION)..."
	@GOBIN=$(LOCALBIN) go install github.com/mikefarah/yq/v4@$(YQ_VERSION) && mv $(LOCALBIN)/yq $(YQ)

# go-install-tool will 'go install' any package with custom target and target name.
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3); \
echo "Installing $${package}"; \
GOBIN=$(LOCALBIN) go install $${package}; \
mv -f $$(echo "$(1)" | sed "s/-$(3)$$//") $(1); \
}
endef
