package operator

import (
	"context"
	"errors"
	"testing"

	opv1 "github.com/openshift/api/operator/v1"
	"github.com/openshift/csi-operator/pkg/operator/config"
	"github.com/openshift/library-go/pkg/operator/v1helpers"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestValidatePreCondition(t *testing.T) {
	testProvider := "test-driver.csi.k8s.io"

	cases := []struct {
		name                     string
		cfg                      *config.OperatorControllerConfig
		expectSyncError          bool
		expectControllersRunning bool
	}{
		{
			name: "Precondition valid without error",
			cfg: &config.OperatorControllerConfig{
				Precondition: func() (bool, error) { return true, nil },
			},
			expectSyncError:          false,
			expectControllersRunning: true,
		},
		{
			name: "Precondition valid with error from internal operation",
			cfg: &config.OperatorControllerConfig{
				Precondition: func() (bool, error) { return true, errors.New("") },
			},
			expectSyncError:          true,
			expectControllersRunning: true,
		},
		{
			name: "Precondition not valid without error",
			cfg: &config.OperatorControllerConfig{
				Precondition: func() (bool, error) { return false, nil },
			},
			expectSyncError:          false,
			expectControllersRunning: false,
		},
		{
			name: "Precondition not valid with error from internal operation",
			cfg: &config.OperatorControllerConfig{
				Precondition: func() (bool, error) { return false, errors.New("") },
			},
			expectSyncError:          false,
			expectControllersRunning: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operator := &FakeOperator{
				ObjectMeta: metav1.ObjectMeta{Name: testProvider},
				Spec:       opv1.OperatorSpec{ManagementState: opv1.Managed},
			}
			c := &Controller{
				operatorClient:           v1helpers.NewFakeOperatorClientWithObjectMeta(&operator.ObjectMeta, &operator.Spec, &operator.Status, nil),
				operatorControllerConfig: tc.cfg,
			}
			ctx := context.Background()
			err := c.sync(ctx, nil)
			if c.controllersRunning != tc.expectControllersRunning {
				t.Fatalf("Unexpected CSI controller running state: %v", c.controllersRunning)
			}
			if err != nil && !tc.expectSyncError {
				t.Fatalf("Did not expect an error when syncing")
			}
		})
	}
}
