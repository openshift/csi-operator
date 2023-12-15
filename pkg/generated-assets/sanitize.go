package generated_assets

import (
	"bytes"
	"fmt"
	"io"

	sigyaml "sigs.k8s.io/yaml"
)

// Sanitize reorders fields in a YAML file in a canonical order, so they can be compared easily with `diff`.
// Preserves comments at the beginning of the file, but all other comments will be lost.
func Sanitize(src []byte) ([]byte, error) {

	comments, err := initialComments(src)
	if err != nil {
		return nil, err
	}

	var obj interface{}
	sigyaml.Unmarshal(src, &obj)
	sanitized, err := sigyaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return bytes.Join([][]byte{comments, sanitized}, []byte{'\n'}), nil
}

// initialComments returns the comments at the beginning of the file.
// We don't expect asset yaml files containing only comments. initialComments() would fail in such cases intentionally.
func initialComments(src []byte) ([]byte, error) {
	buf := bytes.NewBuffer(src)
	comments := bytes.NewBuffer(nil)
	for {
		line, err := buf.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				return nil, fmt.Errorf("failed to separate comments from yaml file: the file has only comments and no yaml")
			}
			return nil, fmt.Errorf("failed to separate comments from yaml file: %w", err)
		}
		if line[0] != '#' {
			return comments.Bytes(), nil
		}
		comments.Write(line)
	}
}

// AssetOrderer organizes assets into three stages based on their kind.
// Its main purpose is to order asset names according to their creation preference.
// For instance, typically RBAC Roles should be created before RoleBindings.
type AssetOrderer struct {
	// stage1 contains standalone resources that are typically referenced by other resources.
	// Examples include ClusterRoles and Roles.
	stage1 []string

	// stage2 contains resources that reference other resources from stage1.
	// Examples include ClusterRoleBindings and RoleBindings.
	stage2 []string

	// stage3 contains resources that should be created last and may depend on resources from stage1 and stage2.
	// Examples include Deployments and DaemonSets.
	stage3 []string
}

// Add adds an asset to the appropriate stage based on its kind.
func (a *AssetOrderer) Add(name, kind string) {
	switch kind {
	case "ClusterRole.rbac.authorization.k8s.io", "Role.rbac.authorization.k8s.io":
		a.stage1 = append(a.stage1, name)
	case "ClusterRoleBinding.rbac.authorization.k8s.io", "RoleBinding.rbac.authorization.k8s.io":
		a.stage2 = append(a.stage2, name)
	default:
		a.stage3 = append(a.stage3, name)
	}
}

// Get returns a concatenated list of assets from all stages.
func (a *AssetOrderer) GetAll() []string {
	result := make([]string, 0, len(a.stage1)+len(a.stage2)+len(a.stage3))
	result = append(result, a.stage1...)
	result = append(result, a.stage2...)
	result = append(result, a.stage3...)
	return result
}
