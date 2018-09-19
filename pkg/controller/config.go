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

package controller

import (
	csi "github.com/openshift/csi-operator/pkg/apis/csidriver/v1alpha1"
	"k8s.io/api/core/v1"
)

// Configuration of the CSI Driver operator.
type Config struct {
	// Default sidecar container images used when CR does not specify anything special.
	DefaultImages csi.CSIDeploymentContainerImages

	// Selector of nodes where Deployment with controller components (provisioner, attacher) can run.
	// When nil, no selector will be set in the Deployment.
	InfrastructureNodeSelector *v1.NodeSelector

	// Number of replicas of Deployment with controller components.
	DeploymentReplicas int

	// Name of cluster role to bind to ServiceAccount that runs all pods with drivers. This role allows to run
	// provisioner, attacher and driver registrar, i.e. read/modify PV, PVC, Node, VolumeAttachment and whatnot in
	// *any* namespace.
	// TODO: should there be multiple ClusterRoles, separate one for provisioner, attacher and driver registrar?
	// In addition, some of them may require variants (e.g. provisioner without access to all secrets and / or attacher
	// without access to all secrets)
	ClusterRoleName string

	// Name of cluster role to bind to ServiceAccount that runs all pods with drivers. This role allows attacher and
	// provisioner to run leader election. It will be bound to the ServiceAccount using RoleBind, i.e. leader election
	// will be possible only in the namespace where the drivers run.
	LeaderElectionClusterRoleName string
}
