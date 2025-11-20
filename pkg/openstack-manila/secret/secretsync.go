package secret

import (
	"context"
	"fmt"
	"time"

	"github.com/gophercloud/utils/v2/openstack/clientconfig"
	operatorv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/openstack-manila/util"
	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/klog/v2"
)

// This SecretSyncController translates Secret provided by cloud-credential-operator into
// format required by the CSI driver.
type SecretSyncController struct {
	operatorClient        v1helpers.OperatorClient
	kubeClient            kubernetes.Interface
	secretLister          corelisters.SecretLister
	eventRecorder         events.Recorder
	controlPlaneNamespace string
	guestNamespace        string
}

const (
	// Name of key with clouds.yaml in Secret provided by cloud-credentials-operator.
	cloudSecretKey = "clouds.yaml"
	// Path for custom CA certificates when provided by cloud-credentials-operator.
	defaultCACertPath = "/etc/openstack/ca.crt"
	// Path for custom CA certificates when provided by Installer (legacy path).
	legacyCACertPath = "/etc/kubernetes/static-pod-resources/configmaps/cloud-config/ca-bundle.pem"
)

func NewSecretSyncController(
	operatorClient v1helpers.OperatorClient,
	kubeClient kubernetes.Interface,
	informers v1helpers.KubeInformersForNamespaces,
	controlPlaneNamespace,
	guestNamespace string,
	resync time.Duration,
	eventRecorder events.Recorder) factory.Controller {

	// Read secret from operator namespace and save the translated one to the operand namespace
	secretInformer := informers.InformersFor(controlPlaneNamespace)
	c := &SecretSyncController{
		operatorClient:        operatorClient,
		kubeClient:            kubeClient,
		secretLister:          secretInformer.Core().V1().Secrets().Lister(),
		eventRecorder:         eventRecorder.WithComponentSuffix("SecretSync"),
		controlPlaneNamespace: controlPlaneNamespace,
		guestNamespace:        guestNamespace,
	}
	return factory.New().WithSync(c.sync).ResyncEvery(resync).WithSyncDegradedOnError(operatorClient).WithInformers(
		operatorClient.Informer(),
		secretInformer.Core().V1().Secrets().Informer(),
	).ToController("SecretSync", eventRecorder)
}

func (c *SecretSyncController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	opSpec, _, _, err := c.operatorClient.GetOperatorState()
	if err != nil {
		return err
	}
	if opSpec.ManagementState != operatorv1.Managed {
		return nil
	}

	cloudSecret, err := c.secretLister.Secrets(c.controlPlaneNamespace).Get(util.CloudCredentialSecretName)
	if err != nil {
		if errors.IsNotFound(err) {
			// TODO: report error after some while?
			klog.V(2).Infof("Waiting for secret %s from cloud-credentials-operator", util.CloudCredentialSecretName)
			return nil
		}
		return err
	}

	driverSecret, err := c.translateSecret(cloudSecret)
	if err != nil {
		return err
	}

	_, _, err = resourceapply.ApplySecret(ctx, c.kubeClient.CoreV1(), c.eventRecorder, driverSecret)
	if err != nil {
		return err
	}
	return nil
}

func (c *SecretSyncController) translateSecret(cloudSecret *v1.Secret) (*v1.Secret, error) {
	content, ok := cloudSecret.Data[cloudSecretKey]
	if !ok {
		return nil, fmt.Errorf("OpenStack credentials secret %s did not contain key %s", util.CloudCredentialSecretName, cloudSecretKey)
	}

	var clouds clientconfig.Clouds
	err := yaml.Unmarshal(content, &clouds)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal clouds credentials stored in secret %s: %s", util.CloudCredentialSecretName, err)
	}

	cloud, ok := clouds.Clouds[util.CloudName]
	if !ok {
		return nil, fmt.Errorf("failed to parse clouds credentials stored in secret %s: cloud %s not found", util.CloudCredentialSecretName, util.CloudName)
	}

	data := cloudToConf(cloud)

	// Determine where our CA cert is stored.
	// TODO(stephenfin): Remove most of this in 4.22
	var caCertPath string
	if _, ok := cloudSecret.Data["cacert"]; ok {
		// Option A: We have the CA cert in our credentials under the 'cacert'
		// key which indicates a recent (>= 4.19) version of
		// cluster-credential-operator (CCO) or hypershift
		caCertPath = defaultCACertPath
	} else if _, ok = cloudSecret.Data["ca-bundle.pem"]; ok {
		// Option B: We have the CA cert in our credentials but under the
		// 'ca-bundle.pem' key, which indicates an older (< 4.19) version of
		// hypershift
		caCertPath = legacyCACertPath
	} else if cloud.CACertFile != "" {
		// Option C: We have a non-empty 'cafile' field in our clouds.yaml.
		// This means our root credential secret has this defined yet
		// cloud-credential-operator (CCO) didn't populate the 'cacert' key of
		// the secret. This indicates an older (< 4.19) version of CCO.
		caCertPath = legacyCACertPath
	}

	if caCertPath != "" {
		data["os-certAuthorityPath"] = []byte(caCertPath)
	}

	secret := v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      util.ManilaSecretName,
			Namespace: c.guestNamespace,
		},
		Type: v1.SecretTypeOpaque,
		Data: data,
	}

	return &secret, nil
}

func cloudToConf(cloud clientconfig.Cloud) map[string][]byte {
	data := make(map[string][]byte)

	if cloud.AuthInfo.AuthURL != "" {
		data["os-authURL"] = []byte(cloud.AuthInfo.AuthURL)
	}
	if cloud.RegionName != "" {
		data["os-region"] = []byte(cloud.RegionName)
	}
	if cloud.AuthInfo.UserID != "" {
		data["os-userID"] = []byte(cloud.AuthInfo.UserID)
	} else if cloud.AuthInfo.Username != "" {
		data["os-userName"] = []byte(cloud.AuthInfo.Username)
	}
	if cloud.AuthInfo.Password != "" {
		data["os-password"] = []byte(cloud.AuthInfo.Password)
	}
	if cloud.AuthInfo.ApplicationCredentialID != "" {
		data["os-applicationCredentialID"] = []byte(cloud.AuthInfo.ApplicationCredentialID)
	}
	if cloud.AuthInfo.ApplicationCredentialName != "" {
		data["os-applicationCredentialName"] = []byte(cloud.AuthInfo.ApplicationCredentialName)
	}
	if cloud.AuthInfo.ApplicationCredentialSecret != "" {
		data["os-applicationCredentialSecret"] = []byte(cloud.AuthInfo.ApplicationCredentialSecret)
	}
	if cloud.AuthInfo.ProjectID != "" {
		data["os-projectID"] = []byte(cloud.AuthInfo.ProjectID)
	} else if cloud.AuthInfo.ProjectName != "" {
		data["os-projectName"] = []byte(cloud.AuthInfo.ProjectName)
	}
	if cloud.AuthInfo.DomainID != "" {
		data["os-domainID"] = []byte(cloud.AuthInfo.DomainID)
	} else if cloud.AuthInfo.DomainName != "" {
		data["os-domainName"] = []byte(cloud.AuthInfo.DomainName)
	}
	if cloud.AuthInfo.ProjectDomainID != "" {
		data["os-projectDomainID"] = []byte(cloud.AuthInfo.ProjectDomainID)
	} else if cloud.AuthInfo.ProjectDomainName != "" {
		data["os-projectDomainName"] = []byte(cloud.AuthInfo.ProjectDomainName)
	}
	if cloud.AuthInfo.UserDomainID != "" {
		data["os-userDomainID"] = []byte(cloud.AuthInfo.UserDomainID)
		data["os-domainID"] = []byte(cloud.AuthInfo.UserDomainID)
	} else if cloud.AuthInfo.UserDomainName != "" {
		data["os-userDomainName"] = []byte(cloud.AuthInfo.UserDomainName)
		data["os-domainName"] = []byte(cloud.AuthInfo.UserDomainName)
	}

	// We don't set os-certAuthorityPath here as it's handled separately.

	return data
}
