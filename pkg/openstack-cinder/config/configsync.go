package config

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"time"

	operatorv1 "github.com/openshift/api/operator/v1"
	configv1listers "github.com/openshift/client-go/config/listers/config/v1"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	ini "gopkg.in/ini.v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"

	"github.com/openshift/csi-operator/pkg/clients"
	"github.com/openshift/csi-operator/pkg/openstack-cinder/util"
)

// configSource stores the location of a config file source
type configSource struct {
	Namespace string
	Name      string
	Key       string
}

type configDestination struct {
	namespace  string
	name       string
	kubeClient kubernetes.Interface
}

// This ConfigSyncController generates the configuration file needed for the Cinder CSI controller
// and driver, including optional configuration from the user, and save it to a config map in a
// well-known location that both services can use
type ConfigSyncController struct {
	operatorClient         v1helpers.OperatorClient
	controlPlaneKubeClient kubernetes.Interface
	guestKubeClient        kubernetes.Interface
	controlPlaneInformers  v1helpers.KubeInformersForNamespaces
	guestInformers         v1helpers.KubeInformersForNamespaces
	infrastructureLister   configv1listers.InfrastructureLister
	controlPlaneNamespace  string
	guestNamespace         string
	eventRecorder          events.Recorder
	isHypershift           bool
}

const (
	resyncInterval = 20 * time.Minute

	infrastructureResourceName = "cluster"
)

// NewConfigSyncController creates a new instance of ConfigSyncController
func NewConfigSyncController(c *clients.Clients, isHypershift bool) (factory.Controller, error) {
	controller := &ConfigSyncController{
		operatorClient:         c.OperatorClient,
		controlPlaneKubeClient: c.ControlPlaneKubeClient,
		guestKubeClient:        c.KubeClient,
		controlPlaneInformers:  c.ControlPlaneKubeInformers,
		guestInformers:         c.KubeInformers,
		infrastructureLister:   c.GetInfraInformer().Lister(),
		controlPlaneNamespace:  c.ControlPlaneNamespace,
		guestNamespace:         c.GuestNamespace,
		eventRecorder:          c.EventRecorder.WithComponentSuffix("ConfigSync"),
		isHypershift:           isHypershift,
	}
	return factory.New().WithSync(
		controller.sync,
	).ResyncEvery(
		resyncInterval,
	).WithSyncDegradedOnError(
		c.OperatorClient,
	).WithInformers(
		c.OperatorClient.Informer(),
		c.GetConfigMapInformer(util.OpenShiftConfigNamespace).Informer(),
		c.GetControlPlaneConfigMapInformer(c.ControlPlaneNamespace).Informer(),
		c.GetConfigMapInformer(c.GuestNamespace).Informer(),
	).ToController("ConfigSync", c.EventRecorder), nil
}

func (c *ConfigSyncController) Name() string {
	return "ConfigSyncController"
}

// sync syncs user-defined configuration from one of the two potential locations to the
// operator-defined location
func (c *ConfigSyncController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	var configSources []configSource
	var configDestinations []configDestination
	var configMapLister corelisters.ConfigMapLister
	var err error

	opSpec, _, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	if opSpec.ManagementState != operatorv1.Managed {
		return nil
	}

	if !c.isHypershift {
		// If we're in a standalone deployment then we try to source potential user-provided
		// configuration from one of two config maps
		infra, err := c.infrastructureLister.Get(infrastructureResourceName)
		if err != nil {
			return err
		}

		configSources = []configSource{
			// First, we try to retrieve from the Cinder CSI-specific config map
			{util.OpenShiftConfigNamespace, "cinder-csi-config", "config"},
			// Failing that, we attempt to retrieve from the cloud provider-specific config map
			// TODO(stephenfin): We should stop retrieving this once Installer creates the new
			// config, which will allow us to simplify things somewhat here
			{util.OpenShiftConfigNamespace, infra.Spec.CloudConfig.Name, infra.Spec.CloudConfig.Key},
		}

		// ...while saving it to the only cluster we have
		configDestinations = []configDestination{
			{c.controlPlaneNamespace, util.CinderConfigName, c.controlPlaneKubeClient},
		}

		// ...and watching the openshift-config namespace
		configMapLister = c.guestInformers.InformersFor(util.OpenShiftConfigNamespace).Core().V1().ConfigMaps().Lister()
	} else {
		// ...and we don't currently support user-defined configuration so there's nothing to set
		// for that

		// ...but we now have two clusters to save to
		configDestinations = []configDestination{
			{c.controlPlaneNamespace, util.CinderConfigName, c.controlPlaneKubeClient},
			{c.guestNamespace, util.CinderConfigName, c.guestKubeClient},
		}

		// ...and we watch the clusters-${NAME} namespace (on the control plane cluster)
		configMapLister = c.controlPlaneInformers.InformersFor(c.controlPlaneNamespace).Core().V1().ConfigMaps().Lister()
	}

	sourceConfig, enableTopology, err := getSourceConfig(configMapLister, configSources...)
	if err != nil {
		return err
	}

	if enableTopology == "" {
		// Fallback to the auto-generated default if no user-provided configuration is given
		enableTopologyFeature, err := enableTopologyFeature()
		if err != nil {
			return err
		}
		enableTopology = strconv.FormatBool(enableTopologyFeature)
	}

	// ...and we watch the clusters-${NAME} namespace (on the control plane cluster)
	configMapLister = c.guestInformers.InformersFor(c.guestNamespace).Core().V1().ConfigMaps().Lister()

	// FIXME(stephenfin): We extract the CA cert from the cloud-provider-config because we know
	// it will exist here in both a standalone (IPI) deployment and in both clusters in a
	// Hypershift deployment. However, this is less than ideal: cloud providers and CSI drivers are
	// now separate things and we should be able to get this information from the credentials
	// secret, along with our clouds.yaml. Fixing this will requires changes to
	// cloud-credential-operator and hypershift (because the latter doesn't currently use the
	// former). Once we have that feature, we should rejig this.
	caCert, err := getCACert(configMapLister, c.guestNamespace, "cloud-provider-config")
	if err != nil {
		return err
	}

	generatedConfig, err := generateConfig(sourceConfig, caCert)
	if err != nil {
		return err
	}

	for _, configDestination := range configDestinations {
		targetConfig := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configDestination.name,
				Namespace: configDestination.namespace,
			},
			Data: map[string]string{
				"cloud.conf":      generatedConfig,
				"enable_topology": enableTopology,
			},
		}
		_, _, err = resourceapply.ApplyConfigMap(ctx, configDestination.kubeClient.CoreV1(), c.eventRecorder, targetConfig)
		if err != nil {
			return err
		}
	}
	return nil
}

// getCACert gets the (optional) embedded CA Cert provided in a config map by either the Installer
// or the hypershift-operator so that we can inject it into our config files
func getCACert(configMapLister corelisters.ConfigMapLister, ns, name string) (*string, error) {
	cm, err := configMapLister.ConfigMaps(ns).Get(name)
	if err != nil {
		if !errors.IsNotFound(err) {
			return nil, err
		}
		return nil, nil
	}
	caCert, ok := cm.Data["ca-bundle.pem"]
	if !ok || len(caCert) == 0 {
		return nil, nil
	}
	return &caCert, nil
}

// getSourceConfig extracts any source configuration present in any of the provided for the legacy,
// in-tree Cinder CSI driver to those used by the external CSI driver that this operator manages
func getSourceConfig(
	configMapLister corelisters.ConfigMapLister,
	sources ...configSource,
) ([]byte, string, error) {
	extractConfig := func(cm *v1.ConfigMap, key string) ([]byte, string, error) {
		// Process the cloud configuration
		content, ok := cm.Data[key]
		if !ok {
			return nil, "", fmt.Errorf("config map %s/%s did not contain key %s", cm.Namespace, cm.Name, key)
		}

		// This value may not be set and that's okay
		enableTopology, _ := cm.Data["enable_topology"]

		return []byte(content), enableTopology, nil
	}

	for _, source := range sources {
		sourceConfig, err := configMapLister.ConfigMaps(source.Namespace).Get(source.Name)
		if err != nil {
			if !errors.IsNotFound(err) {
				return nil, "", err
			}
		} else {
			return extractConfig(sourceConfig, source.Key)
		}
	}

	return nil, "", nil
}

// generateConfig handles generation of config files for the CSI controller and driver. An
// optional, user-defined config file may be provided. If these are provided then the
// '[BlockStorage]' section (and only that section) will be used in generation.
func generateConfig(
	sourceContent []byte,
	caCert *string,
) (string, error) {
	var cfg *ini.File

	if sourceContent != nil {
		var err error
		cfg, err = ini.Load(sourceContent)
		if err != nil {
			return "", fmt.Errorf("failed to generate cloud.conf: %w", err)
		}
	} else {
		cfg = ini.Empty()
	}

	for _, section := range cfg.Sections() {
		// We want to preserve the BlockStorage section and *nothing* else
		if section.Name() == "BlockStorage" {
			// ...and even then, there are some legacy values we don't want
			if key, _ := section.GetKey("trust-device-path"); key != nil {
				section.DeleteKey("trust-device-path")
			}

			// If that was the only key, delete the section.
			if len(section.KeyStrings()) == 0 {
				cfg.DeleteSection(section.Name())
			}
		} else {
			cfg.DeleteSection(section.Name())
		}
	}

	global, err := cfg.NewSection("Global")
	if err != nil {
		return "", err
	}
	for _, o := range []struct{ k, v string }{
		{"use-clouds", "true"},
		{"clouds-file", "/etc/kubernetes/secret/clouds.yaml"},
		{"cloud", "openstack"},
	} {
		_, err = global.NewKey(o.k, o.v)
		if err != nil {
			return "", err
		}
	}

	if caCert != nil {
		// We don't have a (non-deprecated) way to inject the CA cert itself into the config
		// file, so instead we pass a file path. The path itself may look like a magic value but
		// its where we have configured both the deployment (for controller) and daemonset (for
		// driver) assets to mount the cert, if present.
		_, err = global.NewKey("ca-file", "/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem")
		if err != nil {
			return "", err
		}
	}

	// Generate our shiny new config map to save into the operator's namespace
	var buf bytes.Buffer

	_, err = cfg.WriteTo(&buf)
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
