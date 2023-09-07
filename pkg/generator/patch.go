package generator

import (
	"bytes"
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch"
	"sigs.k8s.io/kustomize/kyaml/yaml"
	"sigs.k8s.io/kustomize/kyaml/yaml/merge2"
	sigyaml "sigs.k8s.io/yaml"
)

// Apply strategic merge or json patch to the source YAML.
func (gen *AssetGenerator) applyAssetPatch(sourceYAML []byte, patchAssetName string, extraReplacements []string) ([]byte, error) {
	if strings.HasSuffix(patchAssetName, ".patch") {
		return gen.applyJSONPatch(sourceYAML, patchAssetName, extraReplacements)
	}
	return gen.applyStrategicMergePatch(sourceYAML, patchAssetName, extraReplacements)
}

// Apply strategic merge patch to the source YAML.
func (gen *AssetGenerator) applyStrategicMergePatch(sourceYAML []byte, patchAssetName string, extraReplacements []string) ([]byte, error) {
	patchYAML := gen.mustReadAsset(patchAssetName, extraReplacements)
	opts := yaml.MergeOptions{ListIncreaseDirection: yaml.MergeOptionsListAppend}
	ret, err := merge2.MergeStrings(string(patchYAML), string(sourceYAML), false, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to apply asset %s: %w", patchAssetName, err)
	}
	return []byte(ret), nil
}

// Add a sidecar to the source YAML, possibly with its kube-rbac-proxy when the sidecar reports metrics.
func (gen *AssetGenerator) addSidecar(sourceYAML []byte, sidecarAssetName string, extraReplacements []string, extraArguments []string, flavour ClusterFlavour, assetPatches AssetPatches) ([]byte, error) {
	sidecarYAML := gen.mustReadAsset(sidecarAssetName, extraReplacements)

	sidecarYAML, err := gen.addArguments(sidecarYAML, extraArguments)
	if err != nil {
		return nil, err
	}

	// Apply all assetPatches
	for _, patch := range assetPatches {
		if !patch.ClusterFlavours.Has(flavour) {
			continue
		}
		sidecarYAML, err = gen.applyAssetPatch(sidecarYAML, patch.PatchAssetName, extraReplacements)
		if err != nil {
			return nil, err
		}
	}

	// Apply the sidecar as strategic merge patch against the source YAML. This add the sidecar as a new container
	// into the source deployment YAML.
	opts := yaml.MergeOptions{ListIncreaseDirection: yaml.MergeOptionsListAppend}
	ret, err := merge2.MergeStrings(string(sidecarYAML), string(sourceYAML), false, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to apply asset %s: %w", sidecarAssetName, err)
	}

	return []byte(ret), nil
}

// Adds arguments to the first container of the sidecar YAML.
func (gen *AssetGenerator) addArguments(sidecarYAML []byte, extraArguments []string) ([]byte, error) {
	if len(extraArguments) == 0 {
		return sidecarYAML, nil
	}

	// Using json patch to add arguments, so convert everything to JSON
	sidecarJSON, err := sigyaml.YAMLToJSON(sidecarYAML)
	if err != nil {
		return nil, err
	}

	// JSON patch does not allow adding multiple elements to a list at once.
	// So we need to apply a patch for each extra argument.
	finalPatchYAML := bytes.NewBuffer(nil)
	for _, arg := range extraArguments {
		singleArgYAMLPatch := gen.mustReadAsset("common/add_cmdline_arg.yaml.patch", []string{"${EXTRA_ARGUMENTS}", arg})
		finalPatchYAML.Write(singleArgYAMLPatch)
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
	sidecarYAML, err = sigyaml.JSONToYAML(sidecarJSON)
	if err != nil {
		return nil, err
	}
	return sidecarYAML, nil
}

// Apply JSON patch to the source YAML.
func (gen *AssetGenerator) applyJSONPatch(sourceYAML []byte, patchAssetName string, extraReplacements []string) ([]byte, error) {
	patchYAML := gen.mustReadAsset(patchAssetName, extraReplacements)
	patchJSON, err := sigyaml.YAMLToJSON(patchYAML)
	if err != nil {
		return nil, err
	}
	sourceJSON, err := sigyaml.YAMLToJSON(sourceYAML)
	if err != nil {
		return nil, err
	}
	patch, err := jsonpatch.DecodePatch(patchJSON)
	if err != nil {
		return nil, err
	}
	sourceJSON, err = patch.Apply(sourceJSON)
	if err != nil {
		return nil, err
	}
	ret, err := sigyaml.JSONToYAML(sourceJSON)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// Read an asset from the assets package.
func (gen *AssetGenerator) mustReadAsset(assetName string, extraReplacements []string) []byte {
	assetBytes, err := gen.reader(assetName)
	if err != nil {
		panic(err)
	}
	return replaceBytes(assetBytes, append(gen.replacements, extraReplacements...))
}

// Apply all replacements to the source bytes.
func replaceBytes(src []byte, replacements []string) []byte {
	for i := 0; i < len(replacements); i += 2 {
		src = bytes.ReplaceAll(src, []byte(replacements[i]), []byte(replacements[i+1]))
	}
	return src
}
