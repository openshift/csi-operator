# CSI driver deployment operator

This operator deploys and updates a CSI driver in OpenShift or Kubernetes cluster.

**WARNING**: This operator is obsolete and no longer in active development. It failed to attract necessary attention and build a community around. Installation of CSI drivers, its documentation and necessary tooling is responsibility of CSI driver vendors.

## Usage

1. Create namespace openshift-csi-operator for the operator, necessary RBAC rules and service account:
    ```bash
    $ kubectl apply -f deploy/prerequisites
    ```

2. Run the operator:

    * Outside of the cluster (for debugging)
      ```bash
      $ KUBERNETES_CONFIG=/var/run/kubernetes/admin.kubeconfig  bin/csi-operator -v 5
      ```
     
    * As deployment (with image created by "make container"):
      ```bash
      $ kubectl apply -f deploy/operator.yaml
      ```
      
      See [docs/configuration.md](docs/configuration.md) for details how to start the operator and how to configure it.

3. Create CSIDriverDeployment:
    ```bash
    $ kubectl apply -f deploy/samples/hostpath.yaml
    ```
    Using `default` namespace here, but CSIDriverDeployment can be installed to any namespace.

4. Watch the driver installed:
    ```bash
    $ kubectl get all
    NAME                                       READY     STATUS    RESTARTS   AGE
    pod/hostpath-controller-64485ff565-578jj   3/3       Running   0          109s
    pod/hostpath-node-rstmm                    2/2       Running   0          109s
    
    NAME                 TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)   AGE
    service/kubernetes   ClusterIP   10.0.0.1     <none>        443/TCP   6m8s
    
    NAME                           DESIRED   CURRENT   READY     UP-TO-DATE   
    AVAILABLE   NODE SELECTOR   AGE
    daemonset.apps/hostpath-node   1         1         1         1            1           <none>          109s
    
    NAME                                  DESIRED   CURRENT   UP-TO-DATE   AVAILABLE   AGE
    deployment.apps/hostpath-controller   1         1         1            1           109s
    
    NAME                                             DESIRED   CURRENT   READY     AGE
    replicaset.apps/hostpath-controller-64485ff565   1         1         1         109s
    ```

## Details

For each CSIDriverDeployment, the operator creates in the same namespace:

* Deployment with controller-level components: external provisioner and external attacher.
* DaemonSet with node-level components that run on every node in the cluster and mount/unmount volumes on nodes.
* StorageClasses.
* ServiceAccount to run all the components.
* RoleBindings and ClusterRuleBindings for the created ServiceAccount.

See [docs/usage.md](docs/usage.md) for details.

## Limitations

* It's limited to OpenShift 3.11 / Kubernetes 1.11 functionality for now. In future it will create CSIDriver instances, however 1.11 / 1.12 without alpha features is the current target.
* The operator has very limited support for CSI drivers that run long-running daemons in their containers. Such drivers can't be updated using `Rolling` update strategy, as it would kill the long running daemons. **In case a driver uses fuse, killing fuse daemons kills all mounts it created on the node, possibly corrupting application data!**
    * Note that we're open to new update strategies, especially we'd welcome some `Draining` strategy that would drain a node (using a taint?) and update a driver on the node after all pods that use the CSI driver are safely evicted.

## OpenShift vs. Kubernetes
This operator works both in Kubernetes and OpenShift (and any other Kubernetes distribution). There are some OpenShift specific things, e.g. special manifests installed into Dockerfile and objects with "openshift" prefix in various `deploy/` yaml files, however, there is nothing OpenShift-ish in the code itself. It's pure Kubernetes code. We're open for contributions from Kubernetes community or even adding non-OpenShift version of yaml files or Dockerfiles.
