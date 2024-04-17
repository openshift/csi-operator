package operator

import (
	"testing"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/library-go/pkg/operator/management"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
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
