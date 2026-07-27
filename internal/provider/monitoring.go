package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	corev1alpha1 "github.com/openeverest/openeverest/v2/api/core/v1alpha1"
	monitoringv1alpha1 "github.com/openeverest/openeverest/v2/api/monitoring/v1alpha1"
	"github.com/openeverest/openeverest/v2/provider-runtime/controller"
	"github.com/openeverest/provider-percona-xtradb-cluster/internal/common"
	pxcv1 "github.com/percona/percona-xtradb-cluster-operator/pkg/apis/pxc/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	monitoringConfigRefFieldPath = "spec.components.monitoring.customSpec.monitoringConfigName"
	monitoringConfigAPIKeyKey    = "apiKey"
	pxcPMMServerKey              = "pmmserverkey"
	pxcPMMServerToken            = "pmmservertoken"
)

type pmmCustomSpec struct {
	MonitoringConfigName *string `json:"monitoringConfigName,omitempty"`
}

func applyMonitoringSettings(c *controller.Context, pxc *pxcv1.PerconaXtraDBCluster, providerSpec *corev1alpha1.ProviderSpec) error {
	monitoringComponent, ok := c.Instance().Spec.Components[common.ComponentMonitoring]
	if !ok {
		pxc.Spec.PMM = nil
		return nil
	}
	monitoringType := monitoringComponent.Type
	if monitoringType == "" {
		monitoringType = common.MonitoringTypePMM
	}

	if monitoringType != common.MonitoringTypePMM {
		return fmt.Errorf("unsupported monitoring component type %q", monitoringComponent.Type)
	}

	monitoringConfigName, err := monitoringConfigNameFromComponent(monitoringComponent)
	if err != nil {
		return err
	}
	if monitoringConfigName == "" {
		pxc.Spec.PMM = nil
		return nil
	}

	monitoringCfg := &monitoringv1alpha1.MonitoringConfig{}
	if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: c.Namespace(), Name: monitoringConfigName}, monitoringCfg); err != nil {
		return fmt.Errorf("get MonitoringConfig %q: %w", monitoringConfigName, err)
	}
	if monitoringCfg.Spec.Type != monitoringv1alpha1.PMMMonitoringType || monitoringCfg.Spec.PMM == nil {
		return fmt.Errorf("MonitoringConfig %q must be type %q", monitoringConfigName, monitoringv1alpha1.PMMMonitoringType)
	}
	if monitoringCfg.Spec.PMM.CredentialsSecretRef.Name == "" {
		return fmt.Errorf("MonitoringConfig %q must set spec.pmm.credentialsSecretRef.name", monitoringConfigName)
	}

	serverHost, err := pmmServerHostFromURL(monitoringCfg.Spec.PMM.URL)
	if err != nil {
		return fmt.Errorf("resolve PMM server host from MonitoringConfig %q URL: %w", monitoringConfigName, err)
	}

	pmmImage := monitoringImageForComponent(c, providerSpec, monitoringType, monitoringComponent)
	if pmmImage == "" {
		return fmt.Errorf("cannot resolve PMM image for component %q", common.ComponentMonitoring)
	}

	if err := syncPMMCredentials(c, monitoringCfg.Spec.PMM.CredentialsSecretRef.Name, pmmImage); err != nil {
		return err
	}

	pxc.Spec.PMM = &pxcv1.PMMSpec{
		Enabled:           true,
		ServerHost:        serverHost,
		Image:             pmmImage,
		CustomClusterName: c.Name(),
		ImagePullPolicy:   corev1.PullIfNotPresent,
	}
	return nil
}

func monitoringConfigNameFromComponent(component corev1alpha1.ComponentSpec) (string, error) {
	if component.CustomSpec == nil || len(component.CustomSpec.Raw) == 0 {
		return "", nil
	}

	cfg := &pmmCustomSpec{}
	if err := json.Unmarshal(component.CustomSpec.Raw, cfg); err != nil {
		return "", fmt.Errorf("decode monitoring component customSpec: %w", err)
	}
	if cfg.MonitoringConfigName == nil {
		return "", nil
	}

	return *cfg.MonitoringConfigName, nil
}

func pmmServerHostFromURL(rawURL string) (string, error) {
	if rawURL == "" {
		return "", fmt.Errorf("url is empty")
	}
	if !strings.Contains(rawURL, "://") {
		return strings.TrimSuffix(rawURL, "/"), nil
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("missing host")
	}
	return u.Host, nil
}

func syncPMMCredentials(c *controller.Context, credentialsSecretName, pmmImage string) error {
	credentialsSecret := &corev1.Secret{}
	if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: c.Namespace(), Name: credentialsSecretName}, credentialsSecret); err != nil {
		return fmt.Errorf("get PMM credentials Secret %q: %w", credentialsSecretName, err)
	}
	apiKey, ok := credentialsSecret.Data[monitoringConfigAPIKeyKey]
	if !ok || len(apiKey) == 0 {
		return fmt.Errorf("PMM credentials Secret %q must contain non-empty %q key", credentialsSecretName, monitoringConfigAPIKeyKey)
	}

	usersSecretName := "everest-secrets-" + c.Name()
	usersSecret := &corev1.Secret{}
	if err := c.Client().Get(c.Context(), client.ObjectKey{Namespace: c.Namespace(), Name: usersSecretName}, usersSecret); err != nil {
		if apierrors.IsNotFound(err) {
			// The operator creates this secret; retry on next reconcile when it appears.
			return nil
		}
		return fmt.Errorf("get users Secret %q: %w", usersSecretName, err)
	}

	desiredKey := pxcPMMServerKey
	obsoleteKey := pxcPMMServerToken
	if isPMM3Image(pmmImage) {
		desiredKey = pxcPMMServerToken
		obsoleteKey = pxcPMMServerKey
	}

	hasDesired := false
	if usersSecret.Data != nil {
		_, hasDesired = usersSecret.Data[desiredKey]
	}
	needsDesiredUpdate := !hasDesired || !bytes.Equal(usersSecret.Data[desiredKey], apiKey)
	hasObsolete := false
	if usersSecret.Data != nil {
		_, hasObsolete = usersSecret.Data[obsoleteKey]
	}
	if !needsDesiredUpdate && !hasObsolete {
		return nil
	}

	orig := usersSecret.DeepCopy()
	if usersSecret.Data == nil {
		usersSecret.Data = map[string][]byte{}
	}
	usersSecret.Data[desiredKey] = append([]byte(nil), apiKey...)
	delete(usersSecret.Data, obsoleteKey)

	if err := c.Client().Patch(c.Context(), usersSecret, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("sync PMM credentials to Secret %q: %w", usersSecretName, err)
	}

	return nil
}

func isPMM3Image(image string) bool {
	if image == "" {
		return false
	}
	image = strings.SplitN(image, "@", 2)[0]
	i := strings.LastIndex(image, ":")
	if i == -1 || i == len(image)-1 {
		return false
	}
	tag := image[i+1:]
	return strings.HasPrefix(tag, "3.") || tag == "3"
}

func monitoringImageForComponent(c *controller.Context, providerSpec *corev1alpha1.ProviderSpec, monitoringType string, component corev1alpha1.ComponentSpec) string {
	if component.Image != "" {
		return component.Image
	}
	if component.Version != "" {
		if image := imageForComponentTypeVersion(providerSpec, monitoringType, component.Version); image != "" {
			return image
		}
	}

	selectedBundle := c.Instance().Spec.Version
	if selectedBundle == "" {
		selectedBundle = c.Instance().Status.Version
	}
	if selectedBundle == "" {
		selectedBundle = controller.GetDefaultVersionBundleName(providerSpec)
	}
	if selectedBundle != "" {
		if bundle, err := controller.ResolveVersionBundle(providerSpec, selectedBundle); err == nil {
			if version, ok := bundle.Components[common.ComponentMonitoring]; ok {
				if image := imageForComponentTypeVersion(providerSpec, monitoringType, version); image != "" {
					return image
				}
			}
		}
	}

	return defaultImageForComponentType(providerSpec, monitoringType)
}
