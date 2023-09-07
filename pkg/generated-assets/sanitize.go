package generated_assets

import (
	sigyaml "sigs.k8s.io/yaml"
)

// Sanitize reorders fields in a YAML file in a canonical order, so they can be compared easily with `diff`.
func Sanitize(src []byte) ([]byte, error) {
	var obj interface{}
	sigyaml.Unmarshal(src, &obj)
	bytes, err := sigyaml.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}
