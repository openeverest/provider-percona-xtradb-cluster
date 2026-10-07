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

package provider

import (
	"maps"

	"github.com/openeverest/provider-percona-xtradb-cluster/definition/components"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
)

// applyCorootAnnotations annotates every MySQL pod for discovery by the
// Coroot cluster agent (https://docs.coroot.com/databases/mysql). The
// password is read by the agent from the users Secret, never put on the pod.
func applyCorootAnnotations(pxc *pxcv1.PerconaXtraDBCluster, params *components.MonitoringParameters) {
	if params.Coroot == nil || !params.Coroot.Enabled {
		return
	}

	if pxc.Spec.PXC.Annotations == nil {
		pxc.Spec.PXC.Annotations = map[string]string{}
	}
	maps.Copy(pxc.Spec.PXC.Annotations, corootAnnotations(pxc.Spec.SecretsName))
}

// The users Secret has no username key, so the username is plain text; the
// agent takes the password from the Secret.
func corootAnnotations(usersSecretName string) map[string]string {
	return map[string]string{
		"coroot.com/mysql-scrape":                                "true",
		"coroot.com/mysql-scrape-port":                           "3306",
		"coroot.com/mysql-scrape-credentials-username":           monitorUser,
		"coroot.com/mysql-scrape-credentials-secret-name":        usersSecretName,
		"coroot.com/mysql-scrape-credentials-secret-password-key": monitorUser,
		// Use TLS when the server offers it; operator certificates are self-signed.
		"coroot.com/mysql-scrape-param-tls": "preferred",
	}
}
