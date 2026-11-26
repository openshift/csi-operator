package main

import (
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

	efsCreator := volumecreate.NewEFSCreator()
	s3Creator := volumecreate.NewS3FilesCreator()

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
		newCreateCommand("Create EFS volume", "Create a single-zone volume in given zone.", efsCreator, &useLocalAWSCredentials),
		newStatusCommand("Show EFS infrastructure status for this cluster.", efsCreator, &useLocalAWSCredentials),
	)

	s3 := &cobra.Command{
		Use:   "s3",
		Short: "Manage AWS S3 Files test infrastructure",
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	s3.AddCommand(
		newCreateCommand("Create S3 Files infrastructure and write CSI test inputs.", "Create mount targets only for nodes in the given zone.", s3Creator, &useLocalAWSCredentials),
		newDeleteCommand("Delete S3 Files infrastructure created for this cluster.", s3Creator, &useLocalAWSCredentials),
		newStatusCommand("Show S3 Files infrastructure status for this cluster.", s3Creator, &useLocalAWSCredentials),
	)

	cmd.AddCommand(efs, s3)
	return cmd
}

func newCreateCommand(description, singleZoneDescription string, creator volumecreate.Creator, useLocalAWSCredentials *bool) *cobra.Command {
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
			if err := creator.Init(cmd.Context(), config, *useLocalAWSCredentials); err != nil {
				return err
			}
			return creator.Create(cmd.Context(), singleZone)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file.")
	cmd.Flags().StringVar(&singleZone, "single-zone", "", singleZoneDescription)
	return cmd
}

func newDeleteCommand(description string, creator volumecreate.Creator, useLocalAWSCredentials *bool) *cobra.Command {
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
			if err := creator.Init(cmd.Context(), config, *useLocalAWSCredentials); err != nil {
				return err
			}
			return creator.Delete(cmd.Context(), force)
		},
	}
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Path to the kubeconfig file.")
	cmd.Flags().BoolVar(&force, "force", false, "Allow deletion while EFS CSI volumes or s3files-sc PVCs still exist.")
	return cmd
}

func newStatusCommand(description string, creator volumecreate.Creator, useLocalAWSCredentials *bool) *cobra.Command {
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
			if err := creator.Init(cmd.Context(), config, *useLocalAWSCredentials); err != nil {
				return err
			}
			return creator.Status(cmd.Context())
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
