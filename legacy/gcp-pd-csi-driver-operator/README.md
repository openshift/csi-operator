# gcp-pd-csi-driver-operator

An operator to deploy the [GCP PD CSI Driver](https://github.com/openshift/gcp-pd-csi-driver) in OKD.

This operator is installed by the [cluster-storage-operator](https://github.com/openshift/cluster-storage-operator).

# Quick start

Before running the operator manually, you must remove the operator installed by CSO/CVO

```shell
# Scale down CVO and CSO
oc scale --replicas=0 deploy/cluster-version-operator -n openshift-cluster-version
oc scale --replicas=0 deploy/cluster-storage-operator -n openshift-cluster-storage-operator

# Delete operator resources (daemonset, deployments)
oc -n openshift-cluster-csi-drivers delete deployment.apps/gcp-pd-csi-driver-operator deployment.apps/gcp-pd-csi-driver-controller daemonset.apps/gcp-pd-csi-driver-node
```

To build and run the operator locally:

```shell
# Create only the resources the operator needs to run via CLI
oc apply -f https://raw.githubusercontent.com/openshift/cluster-storage-operator/main/assets/csidriveroperators/gcp-pd/08_cr.yaml

# Build the operator
make

# Set the environment variables
export DRIVER_IMAGE=quay.io/openshift/origin-gcp-pd-csi-driver:latest
export PROVISIONER_IMAGE=quay.io/openshift/origin-csi-external-provisioner:latest
export ATTACHER_IMAGE=quay.io/openshift/origin-csi-external-attacher:latest
export RESIZER_IMAGE=quay.io/openshift/origin-csi-external-resizer:latest
export SNAPSHOTTER_IMAGE=quay.io/openshift/origin-csi-external-snapshotter:latest
export NODE_DRIVER_REGISTRAR_IMAGE=quay.io/openshift/origin-csi-node-driver-registrar:latest
export LIVENESS_PROBE_IMAGE=quay.io/openshift/origin-csi-livenessprobe:latest
export KUBE_RBAC_PROXY_IMAGE=quay.io/openshift/origin-kube-rbac-proxy:latest

# Run the operator via CLI
./gcp-pd-csi-driver-operator start --kubeconfig $MY_KUBECONFIG --namespace openshift-cluster-csi-drivers
```

