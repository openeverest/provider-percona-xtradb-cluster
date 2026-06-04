HELM ?= helm

# values to override
IMAGE_PREFIX ?= ghcr.io/openeverest
PROVIDER_IMAGE_NAME ?= provider-percona-xtradb-cluster
VERSION ?= 0.1.0
CHART_VERSION ?= $(VERSION)
DISABLE_TELEMETRY ?= true

.PHONY: prepare-chart
prepare-chart:
	yq eval -i '.version = "${CHART_VERSION}"' Chart.yaml
	yq eval -i '.appVersion = "${VERSION}"' Chart.yaml
	yq eval -i '.image.repository = "$(IMAGE_PREFIX)/$(PROVIDER_IMAGE_NAME)"' values.yaml
	yq eval -i '.image.tag = "$(VERSION)"' values.yaml
	yq eval -i '.operator.disableTelemetry = $(DISABLE_TELEMETRY)' values.yaml
	$(MAKE) deps

.PHONY: release
release: DISABLE_TELEMETRY := true
release: prepare-chart

.PHONY: release-dev
release-dev: PROVIDER_IMAGE_NAME := provider-percona-xtradb-cluster-dev
release-dev: DISABLE_TELEMETRY := true
release-dev: prepare-chart

.PHONY: deps
deps:
	$(HELM) version
	$(HELM) repo add percona https://percona.github.io/percona-helm-charts/
	$(HELM) dependency update .
