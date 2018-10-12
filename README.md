# CSI driver deployment operator

This operator deploys and updates a CSI driver in Kubernetes cluster.

## Usage

1. Create namespace csi-operator for the operator, necessary RBAC rules and service account:
    ```bash
    $ kubectl apply -f deploy/prerequisites
    ```

2. Run the operator:

    * Outside of OpenShift (for debugging)
      ```bash
      $ bin/csi-operator -v 5 -alsologtostderr -kubeconfig=/etc/origin/master/admin.kubeconfig
      ```
     
    * Inside OpenShift (with image created by "make container"):
      ```bash
      $ kubectl apply -f deploy/operator.yaml
      ```

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
* DaemonSet with non-level components that run on every node in the cluster and mount/unmount volumes to nodes.
* StorageClasses.
* ServiceAccount to run all the components.
* RoleBindings and ClusterRuleBindings for the created ServiceAccount.

## Limitations

* It's limited to OpenShift 3.11 / Kubernetes 1.11 functionality for now. In future it will create CSIDriver instances, however it must be possible to run the operator in Kubernetes 1.11 environment.
* The operato has very limited support for CSI drivers that run long-running daemons in their containers. Such drivers can't be updated using `Rolling` update strategy, as it would kill the long running daemons. **In case a driver uses fuse, killing fuse daemons kills all mounts it created on the node, possibly corrupting application data!**
    * Note that we're open to new update strategies, especially we'd welcome some `Draining` strategy that would drain a node (using a taint?) and update a driver on the node afterwards.
