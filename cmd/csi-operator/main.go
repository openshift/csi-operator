package main

import (
	"context"
	"flag"
	"runtime"
	"time"

	"github.com/openshift/csi-operator2/pkg/apis/csidriver/v1alpha1"

	"github.com/openshift/csi-operator2/pkg/controller"
	"github.com/operator-framework/operator-sdk/pkg/sdk"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"k8s.io/api/core/v1"

	"github.com/sirupsen/logrus"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	// for glog
	flag.Parse()

	printVersion()

	//sdk.ExposeMetricsPort()

	resyncPeriod := time.Duration(30) * time.Second
	namespace := v1.NamespaceAll
	sdk.Watch("csidriver.storage.okd.io/v1alpha1", "CSIDriverDeployment", namespace, resyncPeriod)
	sdk.Watch("apps/v1", "Deployment", namespace, resyncPeriod)
	sdk.Watch("apps/v1", "DaemonSet", namespace, resyncPeriod)
	sdk.Watch("v1", "ServiceAccount", namespace, resyncPeriod)
	sdk.Watch("rbac.authorization.k8s.io/v1", "RoleBinding", namespace, resyncPeriod)
	sdk.Watch("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", namespace, resyncPeriod)
	sdk.Watch("storage.k8s.io/v1", "StorageClass", namespace, resyncPeriod)

	handler, err := controller.NewHandler(getConfig())
	if err != nil {
		logrus.Fatalf("Failed to start handler: %s", err)
	}
	sdk.Handle(handler)
	sdk.Run(context.TODO())
}

func getConfig() controller.Config {
	str2ptr := func(str string) *string {
		return &str
	}

	return controller.Config{
		// TODO: get the real image names from somewhere (config file or cmdline args?)
		DefaultImages: v1alpha1.CSIDeploymentContainerImages{
			AttacherImage:        str2ptr("quay.io/k8scsi/csi-attacher:v0.3.0"),
			ProvisionerImage:     str2ptr("quay.io/k8scsi/csi-provisioner:v0.3.1"),
			DriverRegistrarImage: str2ptr("quay.io/k8scsi/driver-registrar:v0.3.0"),
			LivenessProbeImage:   str2ptr("quay.io/k8scsi/livenessprobe:latest"),
		},
		// TODO: get the selector from somewhere
		InfrastructureNodeSelector: nil,
		// Not configurable at all
		DeploymentReplicas:            1,
		ClusterRoleName:               "csidriver",
		LeaderElectionClusterRoleName: "csidriver-controller-leader-election",
		KubeletRootDir:                "/var/lib/kubelet",
	}
}
