package volumecreate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
)

func TestWriteEFSTestInputsMatchesAssets(t *testing.T) {
	dir := t.TempDir()
	storageClass := filepath.Join(dir, "sc.yaml")
	manifest := filepath.Join(dir, "manifest.yaml")
	t.Setenv(storageClassLocation, storageClass)
	t.Setenv(manifestLocation, manifest)

	c := &efsCreator{fsID: "fs-1234"}
	if err := c.writeTestInputs("us-east-1a"); err != nil {
		t.Fatal(err)
	}
	gotStorageClass, err := os.ReadFile(storageClass)
	if err != nil {
		t.Fatal(err)
	}
	gotManifest, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	wantStorageClass := `kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: efs-sc
provisioner: efs.csi.aws.com
mountOptions:
  - tls
parameters:
  provisioningMode: efs-ap
  fileSystemId: fs-1234
  directoryPerms: "700"
  basePath: "/dynamic_provisioning"
allowedTopologies:
- matchLabelExpressions:
  - key: topology.kubernetes.io/zone
    values:
    - us-east-1a
volumeBindingMode: WaitForFirstConsumer
`
	wantManifest := `StorageClass:
  FromExistingClassName: efs-sc
SnapshotClass:
  FromName: true
DriverInfo:
  Name: efs.csi.aws.com
  SupportedSizeRange:
    Min: 1Gi
    Max: 64Ti
  Capabilities:
    persistence: true
    fsGroup: false
    block: false
    exec: true
    volumeLimits: false
    controllerExpansion: false
    nodeExpansion: false
    snapshotDataSource: false
    RWX: true
    topology: false
    multiplePVsSameID: true
`
	if string(gotStorageClass) != wantStorageClass {
		t.Errorf("StorageClass differs from the EFS test asset:\n%s", gotStorageClass)
	}
	if string(gotManifest) != wantManifest {
		t.Errorf("manifest differs from the EFS test asset:\n%s", gotManifest)
	}
}

func TestHasEFSTag(t *testing.T) {
	tags := []efstypes.Tag{{Key: aws.String("kubernetes.io/cluster/test"), Value: aws.String("owned")}}
	if !hasEFSTag(tags, "kubernetes.io/cluster/test", "owned") {
		t.Fatal("expected ownership tag to match")
	}
	if hasEFSTag(tags, "kubernetes.io/cluster/test", "shared") {
		t.Fatal("unexpected ownership tag match")
	}
}

func TestClusterTag(t *testing.T) {
	if got, want := clusterTag("test-cluster"), "kubernetes.io/cluster/test-cluster"; got != want {
		t.Fatalf("clusterTag() = %q, want %q", got, want)
	}
}
