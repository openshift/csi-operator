package main

import (
	"flag"

	"github.com/openshift/csi-operator/assets"
	"github.com/openshift/csi-operator/assets/overlays/aws-ebs"
	"github.com/openshift/csi-operator/pkg/generator"
)

// generator is a tool that generates assets for CSI driver operators.
// It is intended to be used *before* building the operators, using `make update`.
//
// The generated assets will be then compiled into the operator binaries using assets.go.
func main() {
	// TODO: AWS EBS is hardcoded, add support for other CSI driver operators
	flavour := flag.String("flavour", "standalone", "cluster flavour")
	path := flag.String("path", "", "path to save assets")

	flag.Parse()

	cfg := aws_ebs.GetAWSEBSGeneratorConfig()

	gen := generator.NewAssetGenerator(generator.ClusterFlavour(*flavour), cfg, assets.ReadFile)
	a, err := gen.GenerateAssets()
	if err != nil {
		panic(err)
	}

	if err := a.Save(*path); err != nil {
		panic(err)
	}
}
