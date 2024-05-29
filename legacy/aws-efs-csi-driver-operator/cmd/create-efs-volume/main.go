package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"

	"github.com/openshift/library-go/pkg/controller/controllercmd"

	"github.com/openshift/aws-efs-csi-driver-operator/pkg/efscreate"
	"github.com/openshift/aws-efs-csi-driver-operator/pkg/version"
)

var (
	useLocalAWSCredentials bool
)

func main() {
	command := NewOperatorCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create-efs-volume",
		Short: "OpenShift AWS EFS Create EFS Volume",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
			os.Exit(1)
		},
	}

	ctrlCmdConfig := controllercmd.NewControllerCommandConfig(
		"create-efs-volume",
		version.Get(),
		runOperatorWithCredentialsConfig,
	)
	// we don't need leader election and metrics for CLI commands
	ctrlCmdConfig.DisableLeaderElection = true
	ctrlCmdConfig.DisableServing = true

	ctrlCmd := ctrlCmdConfig.NewCommand()
	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Create EFS volume"
	flags := ctrlCmd.Flags()
	flags.BoolVar(&useLocalAWSCredentials, "local-aws-creds", false, "Use local AWS credentials instead of credentials loaded from the OCP cluster.")
	cmd.AddCommand(ctrlCmd)

	return cmd
}

func runOperatorWithCredentialsConfig(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	return efscreate.RunOperator(ctx, controllerConfig, useLocalAWSCredentials)
}
