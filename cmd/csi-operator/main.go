package main

import (
	"flag"
	"os"
	"runtime"

	"github.com/golang/glog"
	"github.com/openshift/csi-operator/pkg/apis"
	opconfig "github.com/openshift/csi-operator/pkg/config"
	"github.com/openshift/csi-operator/pkg/controller"
	"github.com/operator-framework/operator-sdk/pkg/k8sutil"
	sdkVersion "github.com/operator-framework/operator-sdk/version"
	"gopkg.in/yaml.v2"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
)

const (
	inClusterNamespacePath      = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"
	leaderElectionConfigMapName = "csi-operator-leader"
)

var (
	configFile = flag.String("config", "", "Path to configuration yaml file.")

	// Filled by makefile
	version = "unknown"
)

func printVersion() {
	glog.Infof("csi-operator Version: %v", version)
	glog.Infof("Go Version: %s", runtime.Version())
	glog.Infof("Go OS/Arch: %s/%s", runtime.GOOS, runtime.GOARCH)
	glog.Infof("operator-sdk Version: %v", sdkVersion.Version)
}

func main() {
	// for glog
	flag.Set("logtostderr", "true")
	flag.Parse()

	// send klog to glog
	var flags flag.FlagSet
	klog.InitFlags(&flags)
	flags.Set("skip_headers", "true")
	flag.Parse()
	klog.SetOutput(&glogWriter{})

	printVersion()

	cfg := opconfig.DefaultConfig()
	if configFile != nil && *configFile != "" {
		var err error
		cfg, err = opconfig.LoadConfig(*configFile)
		if err != nil {
			glog.Fatalf("Failed to load config file %q: %s", *configFile, err)
		}
	}
	if glog.V(4) {
		cfgText, _ := yaml.Marshal(cfg)
		glog.V(4).Infof("Using config:\n%s", cfgText)
	}

	namespace, err := k8sutil.GetWatchNamespace()
	if err != nil {
		glog.V(3).Infof("WATCH_NAMESPACE is not set, watching all namespaces")
		namespace = ""
	}

	// Get a config to talk to the apiserver
	restConfig, err := config.GetConfig()
	if err != nil {
		glog.Fatal(err)
	}

	opts := manager.Options{
		Namespace: namespace,
	}

	if detectInCluster() {
		opts.LeaderElection = true
		opts.LeaderElectionID = leaderElectionConfigMapName
	} else {
		glog.Warningf("Not running in-cluster, disabling leader election!")
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := manager.New(restConfig, opts)
	if err != nil {
		glog.Fatal(err)
	}

	glog.V(4).Info("Registering Components.")

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		glog.Fatal(err)
	}

	// Setup all Controllers
	if err := controller.AddToManager(mgr, cfg); err != nil {
		glog.Fatal(err)
	}

	glog.V(4).Info("Starting the Cmd.")

	// Start the Cmd
	glog.Info(mgr.Start(signals.SetupSignalHandler()))
}

// Send klog to glog
type glogWriter struct{}

func (file *glogWriter) Write(data []byte) (n int, err error) {
	glog.InfoDepth(0, string(data))
	return len(data), nil
}

func detectInCluster() bool {
	if _, err := os.Stat(inClusterNamespacePath); err != nil {
		return false
	}
	return true
}
