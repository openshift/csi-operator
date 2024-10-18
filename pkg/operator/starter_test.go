package operator

import (
	"path/filepath"
	"testing"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/assets"
	azure_disk "github.com/openshift/csi-operator/pkg/driver/azure-disk"
	"github.com/openshift/csi-operator/pkg/driver/common/operator"
	openstack_manila "github.com/openshift/csi-operator/pkg/driver/openstack-manila"
	generated_assets "github.com/openshift/csi-operator/pkg/generated-assets"
	"github.com/openshift/csi-operator/pkg/generator"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/operator/management"
	"github.com/openshift/library-go/pkg/operator/resource/resourceread"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type FakeOperator struct {
	metav1.ObjectMeta
	Spec   opv1.OperatorSpec
	Status opv1.OperatorStatus
}

func TestGetOperatorSyncState(t *testing.T) {
	deletionTimestamp := metav1.Now()
	testProvider := "test-driver.csi.k8s.io"

	cases := []struct {
		name          string
		operator      *FakeOperator
		removable     bool
		expectedState opv1.ManagementState
	}{
		{
			name: "should return managed when the operator state is managed",
			operator: &FakeOperator{
				ObjectMeta: metav1.ObjectMeta{Name: testProvider},
				Spec:       opv1.OperatorSpec{ManagementState: opv1.Managed},
			},
			removable:     true,
			expectedState: opv1.Managed,
		},
		{
			name: "should return unmanaged when the operator state is unmanaged",
			operator: &FakeOperator{
				ObjectMeta: metav1.ObjectMeta{Name: testProvider},
				Spec:       opv1.OperatorSpec{ManagementState: opv1.Unmanaged},
			},
			removable:     true,
			expectedState: opv1.Unmanaged,
		},
		{
			name: "should return unmanaged when the operator state is removed",
			operator: &FakeOperator{
				ObjectMeta: metav1.ObjectMeta{Name: testProvider},
				Spec:       opv1.OperatorSpec{ManagementState: opv1.Removed},
			},
			removable:     true,
			expectedState: opv1.Unmanaged,
		},
		{
			name: "should return removed when the deletion timestamp is set and operator is removable",
			operator: &FakeOperator{
				ObjectMeta: metav1.ObjectMeta{
					Name:              testProvider,
					DeletionTimestamp: &deletionTimestamp,
				},
				Spec: opv1.OperatorSpec{ManagementState: opv1.Managed},
			},
			removable:     true,
			expectedState: opv1.Removed,
		},
		{
			name: "should return managed when the deletion timestamp is set and operator is not removable",
			operator: &FakeOperator{
				ObjectMeta: metav1.ObjectMeta{
					Name:              testProvider,
					DeletionTimestamp: &deletionTimestamp,
				},
				Spec: opv1.OperatorSpec{ManagementState: opv1.Managed},
			},
			removable:     false,
			expectedState: opv1.Managed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operatorClient := v1helpers.NewFakeOperatorClientWithObjectMeta(&tc.operator.ObjectMeta, &tc.operator.Spec, &tc.operator.Status, nil)
			if tc.removable {
				management.SetOperatorRemovable()
			} else {
				management.SetOperatorNotRemovable()
			}
			state := getOperatorSyncState(operatorClient)
			if state != tc.expectedState {
				t.Fatalf("expected sync state to be %v, got %v", tc.expectedState, state)
			}
		})
	}
}

func TestDefaultReplacements(t *testing.T) {
	cases := []struct {
		name                  string
		opConfig              *config.OperatorConfig
		flavor                generator.ClusterFlavour
		controlPlaneNamespace string
		GuestNamespace        string
	}{
		{
			name:                  "Testing replacement of Azure disk assets",
			opConfig:              azure_disk.GetAzureDiskOperatorConfig(),
			flavor:                generator.FlavourStandalone,
			controlPlaneNamespace: "openshift-cluster-csi-drivers",
			GuestNamespace:        "openshift-cluster-csi-drivers",
		},
		{
			name:                  "Testing replacement of Azure disk assets with Hypershift",
			opConfig:              azure_disk.GetAzureDiskOperatorConfig(),
			flavor:                generator.FlavourHyperShift,
			controlPlaneNamespace: "clusters-foo",
			GuestNamespace:        "openshift-cluster-csi-drivers",
		},
		{
			name:                  "Testing replacement of OpenStack manila assets without Hypershift",
			opConfig:              openstack_manila.GetOpenStackManilaOperatorConfig(),
			flavor:                generator.FlavourStandalone,
			controlPlaneNamespace: "openshift-manila-csi-driver",
			GuestNamespace:        "openshift-manila-csi-driver",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assetDir := filepath.Join(tc.opConfig.AssetDir, string(tc.flavor))
			a, err := generated_assets.NewFromAssets(assets.ReadFile, assetDir)
			if err != nil {
				t.Fatalf("Failure when reading assets %v", err)
			}

			defaultReplacements := operator.DefaultReplacements(tc.controlPlaneNamespace, tc.GuestNamespace)
			a.SetReplacements(defaultReplacements)

			nodeAsset, err := a.GetAsset("node.yaml")
			if err != nil {
				t.Fatalf("Failure when getting populated asset %v", err)
			}
			generatedNameSpace := getTestDaemonSet(nodeAsset).Namespace
			if generatedNameSpace != tc.GuestNamespace {
				t.Fatalf("expected generated DaemonSet to use namespace %v, got %v", tc.GuestNamespace, generatedNameSpace)
			}

			controllerAsset, err := a.GetAsset("controller.yaml")
			if err != nil {
				t.Fatalf("Failure when getting populated asset %v", err)
			}
			generatedNameSpace = getTestDeployment(controllerAsset).Namespace
			if generatedNameSpace != tc.controlPlaneNamespace {
				t.Fatalf("expected generated Deployment to use namespace %v, got %v", tc.controlPlaneNamespace, generatedNameSpace)
			}
		})
	}
}

func getTestDaemonSet(content []byte) *appsv1.DaemonSet {
	return resourceread.ReadDaemonSetV1OrDie(content)
}

func getTestDeployment(content []byte) *appsv1.Deployment {
	return resourceread.ReadDeploymentV1OrDie(content)
}
