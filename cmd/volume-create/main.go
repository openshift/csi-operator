package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/component-base/cli"

	"github.com/openshift/csi-operator/pkg/volumecreate"
)

func main() {
	os.Exit(cli.Run(newCommand()))
}

func newCommand() *cobra.Command {
	var useLocalAWSCredentials bool

	cmd := &cobra.Command{
		Use:   "volume-create",
		Short: "Create AWS CSI test volumes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.PersistentFlags().BoolVar(&useLocalAWSCredentials, "local-aws-creds", false, "Use local AWS credentials instead of credentials loaded from the OCP cluster.")

	efs := &cobra.Command{
		Use:   "efs",
		Short: "Manage AWS EFS test volumes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	efs.AddCommand(
		newOperationCommand(operationOptions{operation: "create", description: "Create EFS volume", singleZoneDescription: "Create a single-zone volume in given zone.", backend: efsBackend{}}, &useLocalAWSCredentials),
		newOperationCommand(operationOptions{operation: "describe", description: "Describe EFS infrastructure created for this cluster.", backend: efsBackend{}}, &useLocalAWSCredentials),
	)

	s3 := &cobra.Command{
		Use:   "s3",
		Short: "Manage AWS S3 Files test infrastructure",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	s3.AddCommand(
		newOperationCommand(operationOptions{operation: "create", description: "Create S3 Files infrastructure and write CSI test inputs.", singleZoneDescription: "Create mount targets only for workers in the given zone.", backend: s3Backend{}}, &useLocalAWSCredentials),
		newOperationCommand(operationOptions{operation: "delete", description: "Delete S3 Files infrastructure created for this cluster.", forceDescription: "Allow deletion while EFS CSI volumes or s3files-sc PVCs still exist.", backend: s3Backend{}}, &useLocalAWSCredentials),
		newOperationCommand(operationOptions{operation: "describe", description: "Describe S3 Files infrastructure created for this cluster.", backend: s3Backend{}}, &useLocalAWSCredentials),
	)

	cmd.AddCommand(efs, s3)
	return cmd
}

type operationBackend interface {
	run(context.Context, *rest.Config, bool, string, bool, string) error
}

type operationOptions struct {
	operation             string
	description           string
	singleZoneDescription string
	forceDescription      string
	backend               operationBackend
}

type efsBackend struct{}

func (efsBackend) run(ctx context.Context, config *rest.Config, localCredentials bool, singleZone string, _ bool, operation string) error {
	return volumecreate.RunEFS(ctx, config, localCredentials, singleZone, operation)
}

type s3Backend struct{}

func (s3Backend) run(ctx context.Context, config *rest.Config, localCredentials bool, singleZone string, force bool, operation string) error {
	if operation == "describe" {
		operation = "status"
	}
	return volumecreate.Run(ctx, config, localCredentials, singleZone, force, operation)
}

func newOperationCommand(options operationOptions, useLocalAWSCredentials *bool) *cobra.Command {
	var kubeconfig, singleZone string
	var force bool
	cmd := &cobra.Command{
		Use:   options.operation,
		Short: options.description,
		Long:  options.description,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
			if err != nil {
				return err
			}
			return options.backend.run(cmd.Context(), config, *useLocalAWSCredentials, singleZone, force, options.operation)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file.")
	if options.singleZoneDescription != "" {
		cmd.Flags().StringVar(&singleZone, "single-zone", "", options.singleZoneDescription)
	}
	if options.forceDescription != "" {
		cmd.Flags().BoolVar(&force, "force", false, options.forceDescription)
	}
	return cmd
}
