package main

import (
	"context"
	"flag"
	"runtime"
	"time"

	"github.com/openshift/csi-operator/pkg/config"
	"github.com/openshift/csi-operator/pkg/controller"
	"github.com/operator-framework/operator-sdk/pkg/sdk"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"github.com/sirupsen/logrus"
	"k8s.io/api/core/v1"
)

func printVersion() {
	logrus.Infof("Go Version: %s", runtime.Version())
	logrus.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	logrus.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

var (
	configFile = flag.String("config", "", "Path to configuration yaml file")
)

func main() {
	// for glog
	flag.Parse()

	printVersion()

	//sdk.ExposeMetricsPort()
	cfg := config.DefaultConfig()
	if configFile != nil && *configFile != "" {
		var err error
		cfg, err = config.LoadConfig(*configFile)
		if err != nil {
			logrus.Fatalf("Failed to load config file %q: %s", *configFile, err)
		}
	}

	resyncPeriod := time.Duration(30) * time.Second
	namespace := v1.NamespaceAll

	sdk.Watch("csidriver.storage.openshift.io/v1alpha1", "CSIDriverDeployment", namespace, resyncPeriod)
	sdk.Watch("apps/v1", "Deployment", namespace, resyncPeriod)
	sdk.Watch("apps/v1", "DaemonSet", namespace, resyncPeriod)
	sdk.Watch("v1", "ServiceAccount", namespace, resyncPeriod)
	sdk.Watch("rbac.authorization.k8s.io/v1", "RoleBinding", namespace, resyncPeriod)
	sdk.Watch("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", namespace, resyncPeriod)
	sdk.Watch("storage.k8s.io/v1", "StorageClass", namespace, resyncPeriod)

	handler, err := controller.NewHandler(cfg)
	if err != nil {
		logrus.Fatalf("Failed to start handler: %s", err)
	}
	sdk.Handle(handler)
	sdk.Run(context.TODO())
}
