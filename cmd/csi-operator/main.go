package main

import (
	"context"
	"flag"
	"runtime"
	"time"

	"github.com/openshift/csi-operator/version"

	"github.com/golang/glog"
	"github.com/openshift/csi-operator/pkg/config"
	"github.com/openshift/csi-operator/pkg/controller"
	"github.com/operator-framework/operator-sdk/pkg/sdk"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
	"k8s.io/api/core/v1"
)

func printVersion() {
	glog.V(2).Infof("csi-operator: %s", version.Version)
	glog.V(4).Infof("Go Version: %s", runtime.Version())
	glog.V(4).Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	glog.V(4).Infof("operator-sdk Version: %v", sdkVersion.Version)
}

var (
	configFile = flag.String("config", "", "Path to configuration yaml file")
)

func main() {
	// for glog
	flag.Set("logtostderr", "true")
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
	if glog.V(4) {
		cfgText, _ := yaml.Marshal(cfg)
		glog.V(4).Infof("Using config:\n%s", cfgText)
	}

	resyncPeriod := time.Duration(30) * time.Second
	namespace := v1.NamespaceAll
	// Watch only things with OwnerLabelName label
	ownedSelectorString := controller.OwnerLabelName

	sdk.Watch("csidriver.storage.openshift.io/v1alpha1", "CSIDriverDeployment", namespace, resyncPeriod)
	sdk.Watch("apps/v1", "Deployment", namespace, resyncPeriod, sdk.WithLabelSelector(ownedSelectorString))
	sdk.Watch("apps/v1", "DaemonSet", namespace, resyncPeriod, sdk.WithLabelSelector(ownedSelectorString))
	sdk.Watch("v1", "ServiceAccount", namespace, resyncPeriod, sdk.WithLabelSelector(ownedSelectorString))
	sdk.Watch("rbac.authorization.k8s.io/v1", "RoleBinding", namespace, resyncPeriod, sdk.WithLabelSelector(ownedSelectorString))
	sdk.Watch("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", namespace, resyncPeriod, sdk.WithLabelSelector(ownedSelectorString))
	sdk.Watch("storage.k8s.io/v1", "StorageClass", namespace, resyncPeriod, sdk.WithLabelSelector(ownedSelectorString))

	handler, err := controller.NewHandler(cfg)
	if err != nil {
		logrus.Fatalf("Failed to start handler: %s", err)
	}
	sdk.Handle(handler)
	sdk.Run(context.TODO())
}
