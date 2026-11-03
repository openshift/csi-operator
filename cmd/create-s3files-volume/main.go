package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"k8s.io/utils/clock"

	"github.com/openshift/csi-operator/pkg/s3filescreate"
	"github.com/openshift/csi-operator/pkg/version"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
)

func main() {
	os.Exit(cli.Run(newCommand()))
}

func newCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create-s3files-volume",
		Short: "Manage AWS S3 Files test infrastructure",
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Help()
		},
	}

	cmd.AddCommand(
		newOperationCommand("create", "Create S3 Files infrastructure and write CSI test inputs."),
		newOperationCommand("status", "Describe S3 Files infrastructure created for this cluster."),
		newOperationCommand("delete", "Delete S3 Files infrastructure created for this cluster."),
	)
	return cmd
}

func newOperationCommand(operation, description string) *cobra.Command {
	var useLocalAWSCredentials, force bool
	var singleZone string

	config := controllercmd.NewControllerCommandConfig("create-s3files-volume", version.Get(), func(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
		return s3filescreate.Run(ctx, controllerConfig, useLocalAWSCredentials, singleZone, force, operation)
	}, clock.RealClock{})
	config.DisableLeaderElection = true
	config.DisableServing = true
	cmd := config.NewCommand()
	cmd.Use = operation
	cmd.Short = description
	cmd.Long = description
	cmd.Flags().BoolVar(&useLocalAWSCredentials, "local-aws-creds", false, "Use local AWS credentials instead of credentials loaded from the OCP cluster.")
	if operation == "create" {
		cmd.Flags().StringVar(&singleZone, "single-zone", "", "Create mount targets only for workers in the given zone.")
	}
	if operation == "delete" {
		cmd.Flags().BoolVar(&force, "force", false, "Allow deletion while EFS CSI volumes or s3files-sc PVCs still exist.")
	}
	// This helper does not consume the generic controller configuration object.
	// Keep the inherited flag accepted for compatibility, but do not advertise it
	// as a useful alternative to --kubeconfig.
	if err := cmd.Flags().MarkHidden("config"); err != nil {
		panic(err)
	}
	return cmd
}
