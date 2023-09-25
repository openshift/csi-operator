package generator

import (
	"bytes"
	"fmt"
	"path/filepath"

	jsonpatch "github.com/evanphx/json-patch"
	"sigs.k8s.io/kustomize/kyaml/yaml"
	"sigs.k8s.io/kustomize/kyaml/yaml/merge2"
	sigyaml "sigs.k8s.io/yaml"
)

// YAMLWithHistory is a YAML file with a log of applied patches.
// The log will be printed as comments at the beginning of the file
// in Render()
type YAMLWithHistory struct {
	yaml    []byte
	history []string
}

// NewYAMLWithHistoryFromAsset loads a YAML file from the given asset name.
// Its history is empty.
func NewYAMLWithHistoryFromAsset(reader AssetReader, assetName string, replacements []string) *YAMLWithHistory {
	assetBytes, err := reader(assetName)
	if err != nil {
		panic(err)
	}

	finalYAML := replaceBytes(assetBytes, replacements)
	y := &YAMLWithHistory{
		yaml:    finalYAML,
		history: []string{},
	}
	return y
}

func (y *YAMLWithHistory) Logf(msg string, args ...any) {
	y.history = append(y.history, fmt.Sprintf(msg, args...))
}

// Render returns the YAML with the history log as comments at the beginning of the file.
func (y *YAMLWithHistory) Render() []byte {
	buf := bytes.NewBuffer(nil)
	for _, log := range y.history {
		if len(log) > 0 {
			fmt.Fprintf(buf, "# %s\n", log)
		} else {
			// Do not produce empty whitespace at the end of empty lines.
			fmt.Fprintf(buf, "#\n")
		}
	}
	// Add some empty lines at the end of the log to separate it from other comments in the yaml file.
	buf.WriteString("#\n")
	buf.WriteString("#\n")
	buf.Write(y.yaml)
	return buf.Bytes()
}

func (y *YAMLWithHistory) ApplyStrategicMergePatch(patchAssetName string, patch *YAMLWithHistory) error {
	opts := yaml.MergeOptions{ListIncreaseDirection: yaml.MergeOptionsListAppend}
	finalYAML, err := merge2.MergeStrings(string(patch.yaml), string(y.yaml), false, opts)
	if err != nil {
		return fmt.Errorf("failed to apply asset %s: %w", patchAssetName, err)
	}
	y.yaml = []byte(finalYAML)

	y.mergeHistory(filepath.Base(patchAssetName), patch)
	y.Logf("Applied strategic merge patch %s", patchAssetName)
	return nil
}

func (y *YAMLWithHistory) ApplyJSONPatch(patchAssetName string, patch *YAMLWithHistory) error {
	patchJSON, err := sigyaml.YAMLToJSON(patch.yaml)
	if err != nil {
		return err
	}
	sourceJSON, err := sigyaml.YAMLToJSON(y.yaml)
	if err != nil {
		return err
	}
	jpatch, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return err
	}
	sourceJSON, err = jpatch.Apply(sourceJSON)
	if err != nil {
		return err
	}
	finalYAML, err := sigyaml.JSONToYAML(sourceJSON)
	if err != nil {
		return err
	}
	y.yaml = finalYAML
	y.mergeHistory(filepath.Base(patchAssetName), patch)
	y.Logf("Applied JSON patch %s", patchAssetName)
	return nil
}

func (y *YAMLWithHistory) mergeHistory(prefix string, patch *YAMLWithHistory) {
	for _, log := range patch.history {
		y.Logf("%s: %s", prefix, log)
	}
}
