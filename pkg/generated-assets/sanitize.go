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
