# CSI driver deployment operator

This operator deploys a CSI driver into Kubernetes cluster.

## Usage

1. Deploy RBAC rules needed by the operator:
    ```bash
    $ kubectl apply -f config/rbac
    ```

2. Create CRD for the operator:
    ```bash
    $ kubectl apply -f config/crds
    ```

3. Run the operator:
    ```bash
    $ 
    ```

## Details

For each CSIDriverDeployment, the operator creates in the same namespace:

* Deployment with controller-level components: external provisioner and external attacher.
* DaemonSet with non-level components that run on every node in the cluster and mount/unmount volumes to nodes.
* StorageClasses.
* ServiceAccount to run all the components.
* RoleBindings and ClusterRuleBindings for the created ServiceAccount.