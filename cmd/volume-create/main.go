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
		newCreateCommand("Create EFS volume", "Create a single-zone volume in given zone.", efsBackend{}, &useLocalAWSCredentials),
		newStatusCommand("Show EFS infrastructure status for this cluster.", efsBackend{}, &useLocalAWSCredentials),
	)

	s3 := &cobra.Command{
		Use:   "s3",
		Short: "Manage AWS S3 Files test infrastructure",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	s3.AddCommand(
		newCreateCommand("Create S3 Files infrastructure and write CSI test inputs.", "Create mount targets only for nodes in the given zone.", s3Backend{}, &useLocalAWSCredentials),
		newDeleteCommand("Delete S3 Files infrastructure created for this cluster.", s3Backend{}, &useLocalAWSCredentials),
		newStatusCommand("Show S3 Files infrastructure status for this cluster.", s3Backend{}, &useLocalAWSCredentials),
	)

	cmd.AddCommand(efs, s3)
	return cmd
}

type operationBackend interface {
	run(context.Context, *rest.Config, bool, string, bool, string) error
}

type efsBackend struct{}

func (efsBackend) run(ctx context.Context, config *rest.Config, localCredentials bool, singleZone string, _ bool, operation string) error {
	return volumecreate.RunEFS(ctx, config, localCredentials, singleZone, operation)
}

type s3Backend struct{}

func (s3Backend) run(ctx context.Context, config *rest.Config, localCredentials bool, singleZone string, force bool, operation string) error {
	return volumecreate.RunS3(ctx, config, localCredentials, singleZone, force, operation)
}

func newCreateCommand(description, singleZoneDescription string, backend operationBackend, useLocalAWSCredentials *bool) *cobra.Command {
	var kubeconfig, singleZone string
	cmd := &cobra.Command{
		Use:   "create",
		Short: description,
		Long:  description,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := loadKubeconfig(kubeconfig)
			if err != nil {
				return err
			}
			return backend.run(cmd.Context(), config, *useLocalAWSCredentials, singleZone, false, "create")
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file.")
	cmd.Flags().StringVar(&singleZone, "single-zone", "", singleZoneDescription)
	return cmd
}

func newDeleteCommand(description string, backend operationBackend, useLocalAWSCredentials *bool) *cobra.Command {
	var kubeconfig string
	var force bool
	cmd := &cobra.Command{
		Use:   "delete",
		Short: description,
		Long:  description,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := loadKubeconfig(kubeconfig)
			if err != nil {
				return err
			}
			return backend.run(cmd.Context(), config, *useLocalAWSCredentials, "", force, "delete")
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file.")
	cmd.Flags().BoolVar(&force, "force", false, "Allow deletion while EFS CSI volumes or s3files-sc PVCs still exist.")
	return cmd
}

func newStatusCommand(description string, backend operationBackend, useLocalAWSCredentials *bool) *cobra.Command {
	var kubeconfig string
	cmd := &cobra.Command{
		Use:   "status",
		Short: description,
		Long:  description,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := loadKubeconfig(kubeconfig)
			if err != nil {
				return err
			}
			return backend.run(cmd.Context(), config, *useLocalAWSCredentials, "", false, "status")
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file.")
	return cmd
}

func loadKubeconfig(kubeconfig string) (*rest.Config, error) {
	// An explicit file overrides KUBECONFIG (including multiple merged files)
	// and ~/.kube/config. The deferred loader also supports in-cluster credentials.
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}
