package generator

import (
	"bytes"
	"path/filepath"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	sigyaml "sigs.k8s.io/yaml"
)

// Apply strategic merge or json patch to the source YAML.
func (gen *AssetGenerator) applyAssetPatch(source *YAMLWithHistory, patchAssetName string, extraReplacements []string) error {
	patchAsset := gen.mustReadPatchAsset(patchAssetName, extraReplacements)
	if strings.HasSuffix(patchAssetName, ".patch") {
		return source.ApplyJSONPatch(patchAssetName, patchAsset)
	}
	return source.ApplyStrategicMergePatch(patchAssetName, patchAsset)
}

// Add a sidecar to the source YAML, possibly with its kube-rbac-proxy when the sidecar reports metrics.
func (gen *AssetGenerator) addSidecar(source *YAMLWithHistory, sidecarAssetName string, extraReplacements []string, extraArguments []string, flavour ClusterFlavour, assetPatches AssetPatches) error {
	sidecar := gen.mustReadPatchAsset(sidecarAssetName, extraReplacements)
	// The fill file name would be logged at the end by ApplyStrategicMergePatch below,
	// but add it the beginning for better readability + log just Base() instead of the full path
	// in ApplyStrategicMergePatch below.
	sidecar.Logf("Loaded from %s", sidecarAssetName)

	sidecar, err := gen.addArguments(sidecar, extraArguments)
	if err != nil {
		return err
	}

	// Apply all assetPatches
	for _, patch := range assetPatches {
		if !patch.ClusterFlavours.Has(flavour) {
			continue
		}
		err = gen.applyAssetPatch(sidecar, patch.PatchAssetName, extraReplacements)
		if err != nil {
			return err
		}
	}

	// Apply the sidecar as strategic merge patch against the source YAML. This adds the sidecar as a new container
	// into the source deployment YAML.
	return source.ApplyStrategicMergePatch(filepath.Base(sidecarAssetName), sidecar)
}

// Adds arguments to the first container of the sidecar YAML.
func (gen *AssetGenerator) addArguments(sidecar *YAMLWithHistory, extraArguments []string) (*YAMLWithHistory, error) {
	if len(extraArguments) == 0 {
		return sidecar, nil
	}

	// Using json patch to add arguments, so convert everything to JSON
	sidecarJSON, err := sigyaml.YAMLToJSON(sidecar.yaml)
	if err != nil {
		return nil, err
	}

	// JSON patch does not allow adding multiple elements to a list at once.
	// So we need to apply a patch for each extra argument.
	finalPatchYAML := bytes.NewBuffer(nil)
	for _, arg := range extraArguments {
		singleArgYAMLPatchAsset := gen.mustReadPatchAsset("common/add_cmdline_arg.yaml.patch", []string{"${EXTRA_ARGUMENTS}", arg})
		finalPatchYAML.Write(singleArgYAMLPatchAsset.yaml)
	}

	finalPatchJSON, err := sigyaml.YAMLToJSON(finalPatchYAML.Bytes())
	if err != nil {
		return nil, err
	}
	argsPatch, err := jsonpatch.DecodePatch(finalPatchJSON)
	if err != nil {
		return nil, err
	}
	sidecarJSON, err = argsPatch.Apply(sidecarJSON)
	if err != nil {
		return nil, err
	}
	sidecarYAML, err := sigyaml.JSONToYAML(sidecarJSON)
	if err != nil {
		return nil, err
	}
	sidecar.yaml = sidecarYAML
	sidecar.Logf("Added arguments %v", extraArguments)
	return sidecar, nil
}

// Read an YAMLWithHistory from the assets with empty history.
// The file name will be logged by Apply*Patch.
func (gen *AssetGenerator) mustReadPatchAsset(assetName string, extraReplacements []string) *YAMLWithHistory {
	return NewYAMLWithHistoryFromAsset(gen.reader, assetName, append(gen.replacements, extraReplacements...))
}

// Read an YAMLWithHistory from the assets with a log entry about loading the file path.
func (gen *AssetGenerator) mustReadBaseAsset(assetName string, extraReplacements []string) *YAMLWithHistory {
	y := NewYAMLWithHistoryFromAsset(gen.reader, assetName, append(gen.replacements, extraReplacements...))
	// TODO: add source aws-ebs.go
	y.Logf("Generated file. Do not edit. Update using \"make update\".")
	y.Logf("")
	y.Logf("Loaded from %s", assetName)
	return y
}

// Apply all replacements to the source bytes.
func replaceBytes(src []byte, replacements []string) []byte {
	for i := 0; i < len(replacements); i += 2 {
		src = bytes.ReplaceAll(src, []byte(replacements[i]), []byte(replacements[i+1]))
	}
	return src
}
