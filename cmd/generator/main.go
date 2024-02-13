package main

import (
	"flag"
	"path/filepath"

	"github.com/openshift/csi-operator/assets"
	aws_ebs "github.com/openshift/csi-operator/pkg/driver/aws-ebs"
	azure_disk "github.com/openshift/csi-operator/pkg/driver/azure-disk"
	azure_file "github.com/openshift/csi-operator/pkg/driver/azure-file"
	"github.com/openshift/csi-operator/pkg/generator"
	"k8s.io/klog/v2"
)

// generator is a tool that generates assets for CSI driver operators.
// It is intended to be used *before* building the operators, using `make update`.
//
// The generated assets will be then compiled into the operator binaries using assets.go.
func main() {
	path := flag.String("path", "assets", "path to save assets")
	klog.InitFlags(nil)
	flag.Parse()

	cfgs := collectConfigs()
	for _, cfg := range cfgs {
		for _, flavour := range []generator.ClusterFlavour{generator.FlavourStandalone, generator.FlavourHyperShift} {
			gen := generator.NewAssetGenerator(generator.ClusterFlavour(flavour), cfg, assets.ReadFile)
			a, err := gen.GenerateAssets()
			if err != nil {
				panic(err)
			}

			outputPath := filepath.Join(*path, cfg.OutputDir, string(flavour))
			if err := a.Save(outputPath); err != nil {
				panic(err)
			}
			klog.Infof("Generated %s", outputPath)
		}
	}
}

func collectConfigs() []*generator.CSIDriverGeneratorConfig {
	return []*generator.CSIDriverGeneratorConfig{
		aws_ebs.GetAWSEBSGeneratorConfig(),
		azure_disk.GetAzureDiskGeneratorConfig(),
		azure_file.GetAzureFileGeneratorConfig(),
	}
}
