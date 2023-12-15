package generated_assets

import (
	"reflect"
	"testing"
)

func TestAssetOrdered(t *testing.T) {
	testCases := []struct {
		name          string
		input         map[string]string
		inputOrder    []string
		expectedOrder []string
	}{
		{
			name: "successfuly reorder one asset",
			input: map[string]string{
				"binding.yaml": "RoleBinding.rbac.authorization.k8s.io",
				"sa.yaml":      "ServiceAccount",
				"role.yaml":    "Role.rbac.authorization.k8s.io",
			},
			inputOrder:    []string{"sa.yaml", "binding.yaml", "role.yaml"},
			expectedOrder: []string{"role.yaml", "binding.yaml", "sa.yaml"},
		},
		{
			name: "successfuly reorder all assets",
			input: map[string]string{
				"binding.yaml": "RoleBinding.rbac.authorization.k8s.io",
				"sa.yaml":      "ServiceAccount",
				"role.yaml":    "Role.rbac.authorization.k8s.io",
			},
			inputOrder:    []string{"sa.yaml", "role.yaml", "binding.yaml"},
			expectedOrder: []string{"role.yaml", "binding.yaml", "sa.yaml"},
		},
		{
			name: "no changes",
			input: map[string]string{
				"binding.yaml": "RoleBinding.rbac.authorization.k8s.io",
				"sa.yaml":      "ServiceAccount",
				"role.yaml":    "Role.rbac.authorization.k8s.io",
			},
			inputOrder:    []string{"role.yaml", "binding.yaml", "sa.yaml"},
			expectedOrder: []string{"role.yaml", "binding.yaml", "sa.yaml"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ao := new(AssetOrderer)
			for _, name := range tc.inputOrder {
				kind := tc.input[name]
				ao.Add(name, kind)
			}
			result := ao.GetAll()
			if !reflect.DeepEqual(result, tc.expectedOrder) {
				t.Fatalf("Expected %q, got %q", tc.expectedOrder, result)
			}
		})
	}
}
