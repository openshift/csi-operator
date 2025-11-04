# Manual Snapshot Removal Guide for Azure File CSI Driver

This guide walks you through manually removing volume snapshots created by the Azure File CSI driver. The process involves identifying the snapshot in OpenShift/Kubernetes, locating the corresponding Azure file share snapshot, and then removing both resources.

## Prerequisites

- `oc` or `oc` CLI configured with cluster access
- `az` CLI installed and authenticated
- Appropriate permissions to delete resources in both OpenShift/Kubernetes and Azure


## Step 1: Select and Identify Snapshot in OpenShift

### 1.1 Set Your Snapshot Name

First, set the name of the VolumeSnapshot you want to remove:

```bash
export SNAPSHOT_NAME="azurefile-volume-snapshot" # Change to snapshot name you want to remove
export SNAPSHOT_NAMESPACE="default"  # Change to your namespace
```

### 1.2 Verify the Snapshot Exists

```bash
oc get volumesnapshot $SNAPSHOT_NAME -n $SNAPSHOT_NAMESPACE
```

### 1.3 Get Snapshot Details

Retrieve detailed information about the snapshot:

```bash
oc describe volumesnapshot $SNAPSHOT_NAME -n $SNAPSHOT_NAMESPACE
```

### 1.4 Extract Key Information

Extract the VolumeSnapshotContent name and snapshot handle:

```bash
# Get the VolumeSnapshotContent name
export SNAPSHOT_CONTENT_NAME=$(oc get volumesnapshot $SNAPSHOT_NAME -n $SNAPSHOT_NAMESPACE -o jsonpath='{.status.boundVolumeSnapshotContentName}')

# Get the snapshot handle (Azure snapshot ID)
export AZURE_SNAPSHOT_HANDLE=$(oc get volumesnapshotcontent $SNAPSHOT_CONTENT_NAME -o jsonpath='{.status.snapshotHandle}')

# Get the source PVC name (for reference)
export SOURCE_PVC_NAME=$(oc get volumesnapshot $SNAPSHOT_NAME -n $SNAPSHOT_NAMESPACE -o jsonpath='{.spec.source.persistentVolumeClaimName}')
```

### 1.5 Get Storage Account and File Share Information

From the source PVC, extract storage account and file share details:

```bash
# Get the PV name bound to the PVC
export PV_NAME=$(oc get pvc $SOURCE_PVC_NAME -n $SNAPSHOT_NAMESPACE -o jsonpath='{.spec.volumeName}')

# Get storage account and file share from PV volumeHandle
export VOLUME_HANDLE=$(oc get pv $PV_NAME -o jsonpath='{.spec.csi.volumeHandle}')

# Parse volume handle for Azure File share details
# Volume handle format: #<storage-account>#<file-share>#<resource-group>#
# Extract components (adjust parsing based on your volume handle format)
export STORAGE_ACCOUNT=$(echo $VOLUME_HANDLE | cut -d'#' -f2)
export FILE_SHARE=$(echo $VOLUME_HANDLE | cut -d'#' -f3)
```

**Alternative:** If volume handle parsing doesn't work, check the handle directly:

```bash
oc get pv $PV_NAME -o jsonpath='{.spec.csi.volumeHandle}'
export STORAGE_ACCOUNT=<YOUR_STORAGE_ACCOUNT_NAME>
export FILE_SHARE=<YOUR_FILE_SHARE_NAME>
```

## Step 2: Find the Snapshot in Azure Cloud

### 2.1 Set Azure Resource Variables

Use the values extracted in Step 1:

```bash
# Verify values are set
echo "Storage Account: $STORAGE_ACCOUNT"
echo "File Share: $FILE_SHARE"
echo "Snapshot Content Name: $SNAPSHOT_CONTENT_NAME"
echo "Azure Snapshot Handle: $AZURE_SNAPSHOT_HANDLE"
echo "Source PVC Name: $SOURCE_PVC_NAME"
```

### 2.2 Extract Snapshot Timestamp from Handle

The `AZURE_SNAPSHOT_HANDLE` from Kubernetes has the format: `<sourceVolumeID>#<snapshot-timestamp>`. Extract the timestamp (the last segment after splitting by `#`):

```bash
# Extract the snapshot timestamp from the handle (format: volumeID#timestamp)
# Split by '#' and take the last segment, which is the Azure snapshot timestamp
export SNAPSHOT_TIMESTAMP=$(echo $AZURE_SNAPSHOT_HANDLE | awk -F'#' '{print $NF}')

echo "Expected snapshot timestamp: $SNAPSHOT_TIMESTAMP"
```

### 2.3 Verify the Specific Snapshot

Verify that the snapshot with the extracted timestamp exists:

```bash
# Show details of the specific snapshot
az storage share show \
    --name $FILE_SHARE \
    --account-name $STORAGE_ACCOUNT \
    --snapshot "$SNAPSHOT_TIMESTAMP" \
    --query "{Name:name,SnapshotTime:snapshotTime,ShareQuota:shareQuota}" \
    --output table
```

If this command succeeds, you've confirmed the snapshot exists and is ready for deletion. If it fails, double-check that:
- The snapshot timestamp matches exactly
- The storage account and file share names are correct
- The snapshot hasn't already been deleted

> Older versions of `az` CLI might have different output format. Try removing the `--query` flag and look for snapshot timestamp
> under different key. Try looking for `snapshot` or similar, instead of `snapshotTime`.

---

## Step 3: Remove the Snapshot

### 3.1 Remove Kubernetes/OpenShift Resources

#### Option A: Normal Deletion (if deletionPolicy is Delete)

Deleting the VolumeSnapshot should *NOT* automatically remove the VolumeSnapshotContent or Azure snapshot:

```bash
# Remove finalizers manually if needed
oc patch volumesnapshot $SNAPSHOT_NAME -n $SNAPSHOT_NAMESPACE \
    -p '{"metadata":{"finalizers":[]}}' \
    --type=merge
    
# Delete the VolumeSnapshot
oc delete volumesnapshot $SNAPSHOT_NAME -n $SNAPSHOT_NAMESPACE

# Wait and verify deletion
oc get volumesnapshot $SNAPSHOT_NAME -n $SNAPSHOT_NAMESPACE
```

#### Manual VolumeSnapshotContent Deletion

Now manually delete VolumeSnapshotContent:

```bash
# Remove VolumeSnapshotContent finalizers if needed
oc patch volumesnapshotcontent $SNAPSHOT_CONTENT_NAME \
    -p '{"metadata":{"finalizers":[]}}' \
    --type=merge

# Delete VolumeSnapshotContent
oc delete volumesnapshotcontent $SNAPSHOT_CONTENT_NAME
```

### 3.2 Remove Azure File Share Snapshot

After removing Kubernetes resources, delete the Azure snapshot using the verified snapshot timestamp:

```bash
# Delete the Azure file share snapshot by its timestamp
az storage share delete \
    --name $FILE_SHARE \
    --account-name $STORAGE_ACCOUNT \
    --snapshot "$SNAPSHOT_TIMESTAMP"
```

### 3.3 Verify Removal

Verify all resources are removed:

```bash
# Check Kubernetes resources
echo "Checking VolumeSnapshot..."
oc get volumesnapshot $SNAPSHOT_NAME -n $SNAPSHOT_NAMESPACE 2>&1 || echo "OK - VolumeSnapshot not found (deleted)"

echo "Checking VolumeSnapshotContent..."
oc get volumesnapshotcontent $SNAPSHOT_CONTENT_NAME 2>&1 || echo "OK - VolumeSnapshotContent not found (deleted)"

# Check Azure snapshot
echo "Checking Azure snapshot..."
az storage share show \
    --name $FILE_SHARE \
    --account-name $STORAGE_ACCOUNT \
    --snapshot $SNAPSHOT_TIMESTAMP \
    2>&1 || echo "OK - Azure snapshot not found (deleted)"
```