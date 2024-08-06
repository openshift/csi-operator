package generated_assets

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/openshift/library-go/pkg/operator/resource/resourceapply"
	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
)

const (
	manifestFileName                        = "manifests.yaml"
	ControllerDeploymentAssetName           = "controller.yaml"
	NodeDaemonSetAssetName                  = "node.yaml"
	ControllerMetricServiceAssetName        = "service.yaml"
	ControllerMetricServiceMonitorAssetName = "servicemonitor.yaml"
	NodeMetricServiceAssetName              = "node_service.yaml"
	NodeMetricServiceMonitorAssetName       = "node_servicemonitor.yaml"
	CredentialRequestControllerAssetName    = "credentials.yaml"
)

const (
	deploymentKind          = "Deployment.apps"
	daemonSetKind           = "DaemonSet.apps"
	storageClassKind        = "StorageClass.storage.k8s.io"
	volumeSnapshotClassKind = "VolumeSnapshotClass.snapshot.storage.k8s.io"
	credentialsRequestKind  = "CredentialsRequest.cloudcredential.openshift.io"
)

var (
	notStaticControllerAssets = sets.NewString(deploymentKind, credentialsRequestKind)
	notStaticGuestAssets      = sets.NewString(daemonSetKind, storageClassKind, volumeSnapshotClassKind, credentialsRequestKind)
)

// CSIDriverAssets contains all the assets required to deploy the CSI driver in runtime.
// The assets are stored in memory as raw bytes, and can be retrieved using GetAsset.
// The assets can be pre-generated and saved to a directory using Save function,
// and then restored in runtime using NewFromAssets function.
type CSIDriverAssets struct {
	// All control-plane assets, incl. the controller Deployment.
	// The controller Deployment is stored under the name ControllerDeploymentAssetName.
	ControllerAssets map[string][]byte

	// All assets for the guest cluster, incl. the node DaemonSet.
	// The node DaemonSet is stored under the name NodeDaemonSetAssetName.
	GuestAssets map[string][]byte

	replacer *strings.Replacer
}

// GetAsset returns the asset with the given name.
func (a *CSIDriverAssets) GetAsset(genratedAssetName string) ([]byte, error) {

	asset, err := a.getRawAsset(genratedAssetName)
	if err != nil {
		return nil, err
	}
	if a.replacer == nil {
		return asset, nil
	}
	assetString := a.replacer.Replace(string(asset))
	return []byte(assetString), nil
}

// SetReplacements sets the replacements to be applied to all assets when retrieved using GetAsset.
func (a *CSIDriverAssets) SetReplacements(replacements []string) {
	a.replacer = strings.NewReplacer(replacements...)
}

func (a *CSIDriverAssets) getRawAsset(generatedAssetName string) ([]byte, error) {
	if assetYAML, ok := a.ControllerAssets[generatedAssetName]; ok {
		return assetYAML, nil
	}
	if assetYAML, ok := a.GuestAssets[generatedAssetName]; ok {
		return assetYAML, nil
	}
	return nil, fmt.Errorf("asset %s not found", generatedAssetName)
}

// GetControllerStaticAssetNames returns the generated names of all static assets deployed in the control plane
// namespace or standalone cluster. These assets should be managed by a StaticResourcesController.
// Any Deployment is filtered out from the list, they're supposed to be handled by DeploymentController.
func (a *CSIDriverAssets) GetControllerStaticAssetNames() []string {
	assets := new(AssetOrderer)
	for name, yaml := range a.ControllerAssets {
		kind, err := getYAMLKind(yaml)
		if err != nil {
			panic(err)
		}
		if notStaticControllerAssets.Has(kind) {
			klog.V(4).Infof("Skipping %s %s from controller static assets", kind, name)
			continue
		}
		klog.V(4).Infof("Added %s %s to controller static assets", kind, name)
		assets.Add(name, kind)
	}
	return assets.GetAll()
}

// GetGuestStaticAssetNames returns the generated names of all static assets deployed in the guest cluster (or
// standalone cluster). These assets should be managed by a StaticResourcesController. Any DaemonSet, StorageClass or
// VolumeSnapshotClass is filtered out from the list, they must be handled by their own specific controllers.
func (a *CSIDriverAssets) GetGuestStaticAssetNames() []string {
	assets := new(AssetOrderer)
	for name, yaml := range a.GuestAssets {
		kind, err := getYAMLKind(yaml)
		if err != nil {
			panic(err)
		}
		if notStaticGuestAssets.Has(kind) {
			klog.V(4).Infof("Skipping %s %s from guest static assets", kind, name)
			continue
		}
		klog.V(4).Infof("Added %s %s to guest static assets", kind, name)
		assets.Add(name, kind)
	}
	return assets.GetAll()
}

// GetStorageClassAssetNames returns the names of all generated StorageClass assets.
func (a *CSIDriverAssets) GetStorageClassAssetNames() []string {
	var names []string
	for name, yaml := range a.GuestAssets {
		kind, err := getYAMLKind(yaml)
		if err != nil {
			panic(err)
		}
		if kind == storageClassKind {
			names = append(names, name)
		}
	}
	return names
}

// GetVolumeSnapshotClassAssetNames returns the names of all generated VolumeSnapshotClass assets.
func (a *CSIDriverAssets) GetVolumeSnapshotClassAssetNames() []string {
	var names []string
	for name, yaml := range a.GuestAssets {
		kind, err := getYAMLKind(yaml)
		if err != nil {
			panic(err)
		}
		if kind == volumeSnapshotClassKind {
			names = append(names, name)
		}
	}
	return names
}

// GetCredentialsRequestAssetNames returns the names of all generated CredentialRequest assets.
func (a *CSIDriverAssets) GetCredentialsRequestAssetNames() []string {
	var names []string
	for name, yaml := range a.ControllerAssets {
		kind, err := getYAMLKind(yaml)
		if err != nil {
			panic(err)
		}
		if kind == credentialsRequestKind {
			names = append(names, name)
		}
	}
	return names
}

// AssetsManifest is a structure that is saved into manifest.yaml using Save(). It lists names of all other CSI driver
// assets in the same directory.
// It is public only to be usable by YAML parser / marshaller. It is not intended to be used outside of this package.
type AssetsManifest struct {
	ControllerAssetNames []string `yaml:"controllerStaticAssetNames"`
	GuestAssetNames      []string `yaml:"guestStaticAssetNames"`
}

// Save saves the generated assets to the given directory. To be used by the generator during compile time
// ("make update").
func (a *CSIDriverAssets) Save(path string) error {
	if path != "" {
		if err := os.MkdirAll(path, 0755); err != nil {
			return err
		}
	}
	m := AssetsManifest{
		ControllerAssetNames: assetNames(a.ControllerAssets),
		GuestAssetNames:      assetNames(a.GuestAssets),
	}

	// Write the list in manifest file in stable order
	sort.Strings(m.ControllerAssetNames)
	sort.Strings(m.GuestAssetNames)

	mYAML, err := yaml.Marshal(m)
	if err != nil {
		return fmt.Errorf("failed to marshall manifests: %w", err)
	}
	manifestsFile := filepath.Join(path, manifestFileName)
	if err := os.WriteFile(manifestsFile, mYAML, 0644); err != nil {
		return fmt.Errorf("failed to write manifests: %w", err)
	}

	if err := saveAssets(path, a.ControllerAssets); err != nil {
		return fmt.Errorf("failed to save controller static resources: %w", err)
	}

	if err := saveAssets(path, a.GuestAssets); err != nil {
		return fmt.Errorf("failed to save guest static resources: %w", err)
	}

	return nil
}

func assetNames(assets map[string][]byte) []string {
	var names []string
	for name := range assets {
		names = append(names, name)
	}
	return names
}

func saveAssets(path string, assets map[string][]byte) error {
	for name, assetBytes := range assets {
		path := filepath.Join(path, name)
		data, err := Sanitize(assetBytes)
		if err != nil {
			return fmt.Errorf("failed to sanitize asset %s: %w", name, err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			return fmt.Errorf("failed to write asset %s: %w", name, err)
		}
	}
	return nil
}

// NewFromAssets loads already pre-generated assets from the given directory.
// To be used by a CSI driver operator in runtime to load assets generated in compile-time by "make update".
func NewFromAssets(reader resourceapply.AssetFunc, dir string) (*CSIDriverAssets, error) {
	klog.V(4).Infof("Loading assets from %s", dir)

	manifestsFile := filepath.Join(dir, manifestFileName)
	manifestsBytes, err := reader(manifestsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifests file: %w", err)
	}

	var m AssetsManifest
	if err := yaml.Unmarshal(manifestsBytes, &m); err != nil {
		return nil, fmt.Errorf("failed to unmarshall manifests: %w", err)
	}

	assets := &CSIDriverAssets{}

	if assets.ControllerAssets, err = loadAssetsArray(reader, dir, m.ControllerAssetNames); err != nil {
		return nil, err
	}

	if assets.GuestAssets, err = loadAssetsArray(reader, dir, m.GuestAssetNames); err != nil {
		return nil, err
	}

	return assets, nil
}

func loadAssetsArray(reader resourceapply.AssetFunc, dir string, names []string) (map[string][]byte, error) {
	assets := make(map[string][]byte, len(names))
	for _, name := range names {
		assetBytes, err := loadAsset(reader, dir, name)
		if err != nil {
			return nil, err
		}
		assets[name] = assetBytes
	}
	return assets, nil
}

func loadAsset(reader resourceapply.AssetFunc, dir, assetName string) ([]byte, error) {
	filename := filepath.Join(dir, assetName)
	assetBytes, err := reader(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read asset %s: %w", filename, err)
	}
	klog.V(4).Infof("Loaded asset %s", filename)
	return assetBytes, nil
}

func getYAMLKind(yaml []byte) (string, error) {
	_, gvk, err := scheme.Codecs.UniversalDecoder().Decode(yaml, nil, &unstructured.Unstructured{})
	if err != nil {
		return "", err
	}
	return gvk.GroupKind().String(), nil
}
