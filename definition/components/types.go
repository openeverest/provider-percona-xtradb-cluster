// Copyright (C) 2026 The OpenEverest Contributors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package components contains parameters types for provider component types.
//
// Each struct here corresponds to a component type defined in versions.yaml
// and is converted to an OpenAPI schema during generation.
// Add fields when a component type needs structured parameters beyond
// what the base Instance spec provides.
//
// +k8s:openapi-gen=true
package components

// PXCParameters defines structured parameters for PXC engine components.
// This struct is converted to OpenAPI schema and served via the /schema endpoint.
// Provider users can specify these fields in the Instance's component parameters.
type PXCParameters struct {
	// Configuration is the raw my.cnf-style engine configuration file content.
	// `configuration` is the conventional property name for the engine
	// configuration file inside a component's parameters schema.
	Configuration string `json:"configuration,omitempty"`
}

// MonitoringParameters defines structured parameters for the monitoring component.
// PMM, Prometheus and Coroot are independent and may be enabled together.
type MonitoringParameters struct {
	// MonitoringConfigName specifies the name of the MonitoringConfig resource
	// to use for configuring PMM monitoring.
	// If not specified, PMM monitoring will not be configured.
	MonitoringConfigName *string `json:"monitoringConfigName,omitempty"`
	// Prometheus configures the experimental mysqld_exporter integration.
	Prometheus *PrometheusParameters `json:"prometheus,omitempty"`
	// Coroot configures the experimental Coroot integration.
	Coroot *CorootParameters `json:"coroot,omitempty"`
}

// PrometheusParameters configures a mysqld_exporter sidecar on every MySQL
// pod and a PodMonitor for the Prometheus Operator to scrape it.
type PrometheusParameters struct {
	// Enabled turns the Prometheus integration on.
	Enabled bool `json:"enabled,omitempty"`
	// PodMonitorLabels are added to the PodMonitor so that the Prometheus
	// podMonitorSelector picks it up (e.g. release: kube-prometheus-stack).
	PodMonitorLabels map[string]string `json:"podMonitorLabels,omitempty"`
}

// CorootParameters configures Coroot pod annotations on every MySQL pod so
// the Coroot cluster agent discovers and scrapes the database.
type CorootParameters struct {
	// Enabled turns the Coroot integration on.
	Enabled bool `json:"enabled,omitempty"`
}
