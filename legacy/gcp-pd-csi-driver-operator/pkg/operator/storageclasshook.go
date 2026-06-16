package operator

import (
	"fmt"

	opv1 "github.com/openshift/api/operator/v1"
	oplisterv1 "github.com/openshift/client-go/operator/listers/operator/v1"
	"github.com/openshift/library-go/pkg/operator/csi/csistorageclasscontroller"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/klog/v2"
)

// getKMSKeyHook checks for GCPCSIDriverConfigSpec in the ClusterCSIDriver object.
// If it contains GCPKMSKeyReference, it sets the corresponding parameter in the SC.
// This allows the admin to specify a customer managed key to be used by default.
func getKMSKeyHook(ccdLister oplisterv1.ClusterCSIDriverLister) csistorageclasscontroller.StorageClassHookFunc {
	return func(_ *opv1.OperatorSpec, class *storagev1.StorageClass) error {
		ccd, err := ccdLister.Get(class.Provisioner)
		if err != nil {
			return err
		}

		driverConfig := ccd.Spec.DriverConfig
		if driverConfig.DriverType != opv1.GCPDriverType || driverConfig.GCP == nil {
			klog.V(4).Infof("No GCPCSIDriverConfigSpec defined for %s", class.Provisioner)
			return nil
		}

		kmsKey := driverConfig.GCP.KMSKey
		if kmsKey == nil {
			klog.V(4).Infof("Not setting empty %s parameter in StorageClass %s", diskEncryptionKMSKey, class.Name)
			return nil
		}

		if class.Parameters == nil {
			class.Parameters = map[string]string{}
		}
		// location defaults to "global"
		location := defaultKMSKeyLocation
		if kmsKey.Location != "" {
			location = kmsKey.Location
		}
		value := fmt.Sprintf("projects/%s/locations/%s/keyRings/%s/cryptoKeys/%s", kmsKey.ProjectID, location, kmsKey.KeyRing, kmsKey.Name)
		klog.V(4).Infof("Setting %s = %s in StorageClass %s", diskEncryptionKMSKey, value, class.Name)
		class.Parameters[diskEncryptionKMSKey] = value
		return nil
	}
}
