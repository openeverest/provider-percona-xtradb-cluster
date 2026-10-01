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
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"

	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apicommon "github.com/openeverest/openeverest/v2/api/common/v1alpha1"
	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
)

// Exporter for the pxc component type (metrics: Exporter): a mysqld_exporter
// sidecar in every engine pod through the operator's spec.pxc.sidecars hook,
// the same shape as the operator's own PMM client. It connects over loopback
// as a least-privilege user the provider creates for the "metrics" credential
// profile, and is rendered only while a bound class demands metrics. Adding or
// removing it changes the pod template, so the operator rolls the pods.
const (
	mysqldExporterComponentType = "mysqld-exporter"
	mysqldExporterPort          = 9104
	mysqldExporterPortName      = "metrics"
	mysqlPort                   = 3306

	// metricsCredentialProfile is the credential profile published in
	// status.monitoring.sources.credentials.
	metricsCredentialProfile = "metrics"
	// metricsUser is the MySQL account created for that profile. MySQL reports
	// loopback TCP clients as localhost, so this host admits only connections
	// from inside the pod.
	metricsUser     = "everest_metrics"
	metricsUserHost = "localhost"
	mysqlLoopback   = "127.0.0.1"
	// metricsPasswordAlphabet keeps the password free of characters that
	// mysqld_exporter cannot carry in a DSN ('?', '/', '@').
	metricsPasswordAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	metricsPasswordLength   = 24

	metricsUserJobLabel = "openeverest.io/metrics-user"
	// metricsCredentialsAnnotation on the engine pod template carries the
	// password hash, so a rotated Secret rolls the pods and the sidecar
	// restarts with the new value (env is read once, at container start).
	metricsCredentialsAnnotation = "monitoring.openeverest.io/metrics-credentials"
)

func metricsCredentialSecretName(c *controller.Context) string {
	return c.Name() + "-monitoring-" + metricsCredentialProfile
}

// applyMetricsExporter adds the exporter sidecar to the engine pod template
// (on the same Apply as the rest of the engine CR) while an ExtensionManaged
// binding demands metrics, and removes it and its credentials once no entry
// asks for them. While bindings are still being materialised the live
// sidecar is kept (hold rule): removing and re-adding it would roll the
// engine pods twice for nothing.
func applyMetricsExporter(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, spec *corev1alpha1.ProviderSpec) error {
	demand, err := c.MonitoringDemand()
	if err != nil {
		return err
	}
	if !demand.Metrics {
		if !demand.Settled {
			keepLiveMetricsSidecar(c, pxc)
			return nil
		}
		return cleanupMetricsExporter(c)
	}

	credentials, err := ensureMetricsCredentialSecret(c)
	if err != nil {
		return err
	}
	if err := ensureMetricsUser(c, credentials); err != nil {
		return err
	}

	image := defaultImageForComponentType(spec, mysqldExporterComponentType)
	if image == "" {
		return fmt.Errorf("no image for component type %q in the Provider version catalog", mysqldExporterComponentType)
	}
	if pxc.Spec.PXC.Annotations == nil {
		pxc.Spec.PXC.Annotations = map[string]string{}
	}
	pxc.Spec.PXC.Annotations[metricsCredentialsAnnotation] = credentialsHash(credentials)
	// No probes: a failing sidecar probe would mark the whole engine pod
	// NotReady and pull it out of the proxy.
	pxc.Spec.PXC.Sidecars = append(pxc.Spec.PXC.Sidecars, corev1.Container{
		Name:  mysqldExporterComponentType,
		Image: image,
		// No my.cnf: the flags fill [client].host/user and the env var fills
		// [client].password.
		Args: []string{
			fmt.Sprintf("--mysqld.address=%s:%d", mysqlLoopback, mysqlPort),
			"--mysqld.username=" + metricsUser,
		},
		Env: []corev1.EnvVar{{
			Name: "MYSQLD_EXPORTER_PASSWORD",
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: credentials.Name},
				Key:                  "password",
			}},
		}},
		Ports: []corev1.ContainerPort{{Name: mysqldExporterPortName, ContainerPort: mysqldExporterPort}},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
		},
	})
	return nil
}

// ensureMetricsCredentialSecret returns the Instance-owned Secret for the
// metrics profile, generating the password on first use. It is never
// re-applied, so the password stays stable across reconciles.
func ensureMetricsCredentialSecret(c *controller.Context) (*corev1.Secret, error) {
	existing := &corev1.Secret{}
	err := c.Get(existing, metricsCredentialSecretName(c))
	if err == nil {
		return existing, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	password, err := randomPassword()
	if err != nil {
		return nil, err
	}
	secret := &corev1.Secret{
		ObjectMeta: c.ObjectMeta(metricsCredentialSecretName(c)),
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username": []byte(metricsUser),
			"password": []byte(password),
		},
	}
	if err := c.Apply(secret); err != nil {
		return nil, fmt.Errorf("apply metrics credential Secret: %w", err)
	}
	return secret, nil
}

func randomPassword() (string, error) {
	out := make([]byte, metricsPasswordLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(metricsPasswordAlphabet))))
		if err != nil {
			return "", err
		}
		out[i] = metricsPasswordAlphabet[n.Int64()]
	}
	return string(out), nil
}

func credentialsHash(credentials *corev1.Secret) string {
	sum := sha256.Sum256(credentials.Data["password"])
	return hex.EncodeToString(sum[:])[:8]
}

// metricsUserJobName is keyed on the password so a rotated Secret runs a
// fresh Job while an unchanged one is applied exactly once.
func metricsUserJobName(c *controller.Context, credentials *corev1.Secret) string {
	return c.Name() + "-metrics-user-" + credentialsHash(credentials)
}

// ensureMetricsUser creates or updates the least-privilege MySQL account
// through a Job running the engine image's mysql client with the operator's
// root credentials, once the cluster is Ready. The SQL is idempotent. Image
// and users Secret come from the live CR: the desired one is still being
// built when this runs.
func ensureMetricsUser(c *controller.Context, credentials *corev1.Secret) error {
	live := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(live, c.Name()); err != nil || live.Status.Status != pxcv1.AppStateReady {
		return nil //nolint:nilerr // not ready yet; the PXC watch re-enqueues
	}
	name := metricsUserJobName(c, credentials)
	if exists, err := c.Exists(&batchv1.Job{}, name); err != nil || exists {
		return err
	}

	account := fmt.Sprintf("'%s'@'%s'", metricsUser, metricsUserHost)
	sql := fmt.Sprintf(`CREATE USER IF NOT EXISTS %[1]s IDENTIFIED BY '${METRICS_PASSWORD}' WITH MAX_USER_CONNECTIONS 5;`+
		` ALTER USER %[1]s IDENTIFIED BY '${METRICS_PASSWORD}';`+
		` GRANT PROCESS, REPLICATION CLIENT, SELECT ON *.* TO %[1]s;`, account)
	meta := c.ObjectMeta(name)
	meta.Labels[metricsUserJobLabel] = "true"
	job := &batchv1.Job{
		ObjectMeta: meta,
		Spec: batchv1.JobSpec{
			BackoffLimit: new(int32(3)),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{{
						Name:    "mysql",
						Image:   live.Spec.PXC.Image,
						Command: []string{"sh", "-ec", fmt.Sprintf(`exec mysql -h %s-pxc -P %d -uroot -e "%s"`, c.Name(), mysqlPort, sql)},
						Env: []corev1.EnvVar{
							{Name: "MYSQL_PWD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: live.Spec.SecretsName}, Key: "root",
							}}},
							{Name: "METRICS_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
								LocalObjectReference: corev1.LocalObjectReference{Name: credentials.Name}, Key: "password",
							}}},
						},
					}},
				},
			},
		},
	}
	if err := c.Apply(job); err != nil {
		return fmt.Errorf("apply metrics user Job: %w", err)
	}
	return nil
}

// metricsUserReady reports whether the Job for the current password succeeded.
func metricsUserReady(c *controller.Context, credentials *corev1.Secret) bool {
	job := &batchv1.Job{}
	if err := c.Get(job, metricsUserJobName(c, credentials)); err != nil {
		return false
	}
	return job.Status.Succeeded > 0
}

// cleanupMetricsExporter removes the credential Secret and the user Jobs;
// the sidecar itself disappears from the engine CR because Sync no longer
// renders it. The MySQL account is left in place.
func cleanupMetricsExporter(c *controller.Context) error {
	jobs := &batchv1.JobList{}
	if err := c.List(jobs, client.MatchingLabels{"app.kubernetes.io/instance": c.Name(), metricsUserJobLabel: "true"}); err != nil {
		return err
	}
	for i := range jobs.Items {
		if err := c.Client().Delete(c.Context(), &jobs.Items[i], client.PropagationPolicy(metav1.DeletePropagationBackground)); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: metricsCredentialSecretName(c), Namespace: c.Namespace()}}
	return c.Delete(secret)
}

// keepLiveMetricsSidecar copies the exporter sidecar from the live engine CR.
func keepLiveMetricsSidecar(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster) {
	live := &pxcv1.PerconaXtraDBCluster{}
	if err := c.Get(live, c.Name()); err != nil {
		return
	}
	for _, sc := range live.Spec.PXC.Sidecars {
		if sc.Name == mysqldExporterComponentType {
			pxc.Spec.PXC.Sidecars = append(pxc.Spec.PXC.Sidecars, *sc.DeepCopy())
		}
	}
	if v, ok := live.Spec.PXC.Annotations[metricsCredentialsAnnotation]; ok {
		if pxc.Spec.PXC.Annotations == nil {
			pxc.Spec.PXC.Annotations = map[string]string{}
		}
		pxc.Spec.PXC.Annotations[metricsCredentialsAnnotation] = v
	}
}

func hasMetricsSidecar(pxc *pxcv1.PerconaXtraDBCluster) bool {
	for _, sc := range pxc.Spec.PXC.Sidecars {
		if sc.Name == mysqldExporterComponentType {
			return true
		}
	}
	return false
}

// engineMetricsSources publishes the sidecar's endpoint on the engine pods
// once the live engine CR carries it, the cluster has rolled to Ready and
// the metrics user exists.
func engineMetricsSources(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, s *corev1alpha1.MonitoringSources) {
	if !hasMetricsSidecar(pxc) || pxc.Status.Status != pxcv1.AppStateReady {
		return
	}
	credentials := &corev1.Secret{}
	if err := c.Get(credentials, metricsCredentialSecretName(c)); err != nil || !metricsUserReady(c, credentials) {
		return
	}
	s.Credentials = append(s.Credentials, corev1alpha1.MonitoringCredential{
		Profile:   metricsCredentialProfile,
		SecretRef: apicommon.SecretRef{Name: credentials.Name},
	})
	s.Metrics = append(s.Metrics, corev1alpha1.MetricsEndpoint{
		Component: common.ComponentEngine,
		Kind:      "mysql",
		PodSelector: map[string]string{
			"app.kubernetes.io/name":      "percona-xtradb-cluster",
			"app.kubernetes.io/instance":  c.Name(),
			"app.kubernetes.io/component": "pxc",
		},
		Port:   corev1alpha1.MetricsEndpointPort{Name: mysqldExporterPortName},
		Path:   "/metrics",
		Scheme: "http",
	})
}
