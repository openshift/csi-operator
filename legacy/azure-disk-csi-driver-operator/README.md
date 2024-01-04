# azure-disk-csi-driver-operator

An operator to deploy the [Azure Disk CSI Driver](https://github.com/openshift/azure-disk-csi-driver) in OKD.

This operator is installed by the [cluster-storage-operator](https://github.com/openshift/cluster-storage-operator).

# Quick start

Before running the operator manually, you must remove the operator installed by CSO/CVO

```shell
# Scale down CVO and CSO
oc scale --replicas=0 deploy/cluster-version-operator -n openshift-cluster-version
oc scale --replicas=0 deploy/cluster-storage-operator -n openshift-cluster-storage-operator

# Delete operator resources (daemonset, deployments)
oc -n openshift-cluster-csi-drivers delete deployment.apps/azure-disk-csi-driver-operator deployment.apps/azure-disk-csi-driver-controller daemonset.apps/azure-disk-csi-driver-node
```

To build and run the operator locally:

```shell
# Build the operator
make

# Set kubeconfig and obtain desired version
export KUBECONFIG=<path-to-kubeconfig>
export OPERATOR_IMAGE_VERSION=$(oc get clusterversion/version -o json | jq -r '.status.desired.version')

# Set the environment variables
export DRIVER_IMAGE=quay.io/openshift/origin-azure-disk-csi-driver:latest
export PROVISIONER_IMAGE=quay.io/openshift/origin-csi-external-provisioner:latest
export ATTACHER_IMAGE=quay.io/openshift/origin-csi-external-attacher:latest
export RESIZER_IMAGE=quay.io/openshift/origin-csi-external-resizer:latest
export SNAPSHOTTER_IMAGE=quay.io/openshift/origin-csi-external-snapshotter:latest
export NODE_DRIVER_REGISTRAR_IMAGE=quay.io/openshift/origin-csi-node-driver-registrar:latest
export LIVENESS_PROBE_IMAGE=quay.io/openshift/origin-csi-livenessprobe:latest
export KUBE_RBAC_PROXY_IMAGE=quay.io/openshift/origin-kube-rbac-proxy:latest
export CLUSTER_CLOUD_CONTROLLER_MANAGER_OPERATOR_IMAGE=quay.io/openshift/origin-cluster-cloud-controller-manager-operator:latest


# Run the operator via CLI
./azure-disk-csi-driver-operator start --kubeconfig $KUBECONFIG --namespace openshift-cluster-csi-drivers
```

