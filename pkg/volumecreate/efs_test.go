package volumecreate

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	awsefs "github.com/aws/aws-sdk-go-v2/service/efs"
	efstypes "github.com/aws/aws-sdk-go-v2/service/efs/types"
	"github.com/aws/aws-sdk-go-v2/service/s3files"
)

func TestRunEFSValidatesOperation(t *testing.T) {
	err := RunEFS(context.Background(), nil, false, "", "unsupported")
	if err == nil || !strings.Contains(err.Error(), "unsupported EFS operation") {
		t.Fatalf("RunEFS() = %v, want unsupported operation error", err)
	}
}

func TestCreatorRequestsPropagateContext(t *testing.T) {
	for _, backend := range []string{"efs", "s3"} {
		t.Run(backend, func(t *testing.T) {
			type contextKey struct{}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "test"))
			defer cancel()
			cfg := reviewConfig(t)
			requests := 0
			cfg.HTTPClient = reviewHTTPClient(func(req *http.Request) (*http.Response, error) {
				requests++
				if req.Context().Value(contextKey{}) != "test" {
					t.Error("AWS request did not receive the caller's context")
				}
				cancel()
				return nil, req.Context().Err()
			})
			var err error
			if backend == "efs" {
				c := &efsCreator{efs: awsefs.NewFromConfig(cfg)}
				_, err = c.ownedFileSystems(ctx)
			} else {
				c := &s3FilesCreator{s3files: s3files.NewFromConfig(cfg)}
				_, err = c.ownedFileSystems(ctx)
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("ownedFileSystems() = %v, want context.Canceled", err)
			}
			if requests != 1 {
				t.Fatalf("received %d requests, want 1", requests)
			}
		})
	}
}

func TestEFSStatusWithoutResources(t *testing.T) {
	cfg := reviewConfig(t,
		reviewResponse{operation: "GET /2015-02-01/file-systems", status: 200, body: `{"FileSystems":[]}`},
		reviewResponse{operation: "DescribeSecurityGroups", status: 200, body: `<DescribeSecurityGroupsResponse><securityGroupInfo/></DescribeSecurityGroupsResponse>`},
	)
	c := &efsCreator{
		genericCreator: &genericCreator{infraName: "test", ec2: ec2.NewFromConfig(cfg)},
		efs:            awsefs.NewFromConfig(cfg),
	}
	if err := c.status(context.Background()); err != nil {
		t.Fatalf("status() = %v", err)
	}
}

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
