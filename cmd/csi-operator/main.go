/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"flag"
	"log"

	"github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"

	"github.com/openshift/csi-operator/pkg/apis"
	"github.com/openshift/csi-operator/pkg/controller"
	_ "k8s.io/client-go/plugin/pkg/client/auth/gcp"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/runtime/signals"
)

const (
	// Nr. of replicas of Deployment with controller components.
	// TODO: increase when we pass leader election parameters to pods.
	controllerDeploymentReplicaCount = 1
)

func main() {
	flag.Parse()

	// Get a config to talk to the apiserver
	cfg, err := config.GetConfig()
	if err != nil {
		log.Fatal(err)
	}

	// Create a new Cmd to provide shared dependencies and start components
	mgr, err := manager.New(cfg, manager.Options{})
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Registering Components.")

	// Setup Scheme for all resources
	if err := apis.AddToScheme(mgr.GetScheme()); err != nil {
		log.Fatal(err)
	}

	// Setup all Controllers
	controllerCfg := getConfig()
	if err := controller.Add(mgr, controllerCfg); err != nil {
		log.Fatal(err)
	}

	log.Printf("Starting the Cmd.")

	// Start the Cmd
	log.Fatal(mgr.Start(signals.SetupSignalHandler()))
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
		DeploymentReplicas:            controllerDeploymentReplicaCount,
		ClusterRoleName:               "csidriver",
		LeaderElectionClusterRoleName: "csidriver-controller-leader-election",
		KubeletRootDir:                "/var/lib/kubelet",
	}
}
