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
	"bytes"
	"fmt"

	commonv1alpha1 "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/openeverest/provider-percona-xtradb-cluster/definition"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
)

const (
	// operatorMonitorUser is the user the Percona operator creates for its
	// own monitoring (PMM, HAProxy checks): SELECT, PROCESS, REPLICATION
	// CLIENT, SUPER and RELOAD on *.*. It is the users Secret key too.
	operatorMonitorUser = "monitor"
	// pxcContainerName and mysqlPortName are the container and port the
	// operator gives every PXC pod.
	pxcContainerName = "pxc"
	mysqlPortName    = "mysql"
)

// monitorSecretName is the Instance-owned Secret mirroring the operator's
// monitor user, with the username and password keys the monitoring contract
// fixes. The runtime publishes only Instance-owned credentials, and the
// users Secret also holds root.
func monitorSecretName(instance string) string {
	return instance + "-monitoring-monitor"
}

// syncMonitorCredential mirrors the operator's monitor user into the
// monitor Secret while a bound class requires connections, and deletes the
// mirror once none does. While the requirement is unresolved, the live
// mirror is held. Every Sync copies the current password, so a rotated one
// reaches the agents.
func syncMonitorCredential(c *controller.Context, usersSecretName string) error {
	req, err := c.RequiredSources()
	if err != nil {
		return fmt.Errorf("resolve required monitoring sources: %w", err)
	}
	mirror := &corev1.Secret{}
	hasMirror, err := c.Exists(mirror, monitorSecretName(c.Name()))
	if err != nil {
		return fmt.Errorf("get monitor Secret: %w", err)
	}
	if !req.Connections.Else(hasMirror) {
		if hasMirror {
			if err := c.Delete(mirror); err != nil {
				return fmt.Errorf("delete monitor Secret: %w", err)
			}
		}
		return nil
	}

	users := &corev1.Secret{}
	found, err := c.Exists(users, usersSecretName)
	if err != nil {
		return fmt.Errorf("get users Secret %q: %w", usersSecretName, err)
	}
	password := users.Data[operatorMonitorUser]
	if !found || len(password) == 0 {
		// The operator generates the users Secret on first reconcile.
		return nil
	}
	if hasMirror && bytes.Equal(mirror.Data[corev1alpha1.CredentialSecretKeyPassword], password) &&
		string(mirror.Data[corev1alpha1.CredentialSecretKeyUsername]) == operatorMonitorUser {
		return nil
	}
	secret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(monitorSecretName(c.Name())),
		Type:       corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			corev1alpha1.CredentialSecretKeyUsername: []byte(operatorMonitorUser),
			corev1alpha1.CredentialSecretKeyPassword: password,
		},
	}
	if err := c.Apply(secret); err != nil {
		return fmt.Errorf("apply monitor Secret: %w", err)
	}
	return nil
}

// monitorConnectionSources returns the monitor credential and the PXC
// nodes' connection endpoint while the mirror exists and a node is ready.
func monitorConnectionSources(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) ([]corev1alpha1.MonitoringCredential, []corev1alpha1.ConnectionEndpoint, error) {
	if pxc.Status.PXC.Ready == 0 {
		return nil, nil, nil
	}
	found, err := c.Exists(&corev1.Secret{}, monitorSecretName(c.Name()))
	if err != nil || !found {
		return nil, nil, err
	}
	endpoint := corev1alpha1.ConnectionEndpoint{
		Component: common.ComponentEngine,
		PodSelector: map[string]string{
			controller.InstanceLabel:  c.Name(),
			controller.ComponentLabel: common.ComponentEngine,
		},
		Container: pxcContainerName,
		Port:      intstr.FromString(mysqlPortName),
		Auth:      &corev1alpha1.MetricsEndpointAuth{CredentialProfile: corev1alpha1.CredentialProfileMonitor},
	}
	tls, err := tlsParameters(c.Instance())
	if err != nil {
		return nil, nil, err
	}
	if tlsEnabled(tls.Mode) {
		// The server certificates name the Services, so agents verify the
		// chain only.
		endpoint.TLS = &corev1alpha1.ConnectionTLS{
			Required: tls.Mode == definition.TLSModeRequire,
			CA: &commonv1alpha1.CABundleSource{SecretKeyRef: &commonv1alpha1.SecretKeyRef{
				Name: clientCertificateSecretName(c, tls),
				Key:  caKey,
			}},
		}
	}
	credentials := []corev1alpha1.MonitoringCredential{{
		Profile:   corev1alpha1.CredentialProfileMonitor,
		SecretRef: commonv1alpha1.SecretRef{Name: monitorSecretName(c.Name())},
	}}
	return credentials, []corev1alpha1.ConnectionEndpoint{endpoint}, nil
}
