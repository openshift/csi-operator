package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"k8s.io/utils/clock"

	"github.com/openshift/library-go/pkg/controller/controllercmd"

	"github.com/openshift/csi-operator/pkg/efscreate"
	"github.com/openshift/csi-operator/pkg/version"
)

var (
	useLocalAWSCredentials bool
	singleZone             string
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
		clock.RealClock{},
	)
	// we don't need leader election and metrics for CLI commands
	ctrlCmdConfig.DisableLeaderElection = true
	ctrlCmdConfig.DisableServing = true

	ctrlCmd := ctrlCmdConfig.NewCommand()
	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Create EFS volume"
	flags := ctrlCmd.Flags()
	flags.BoolVar(&useLocalAWSCredentials, "local-aws-creds", false, "Use local AWS credentials instead of credentials loaded from the OCP cluster.")
	flags.StringVar(&singleZone, "single-zone", "", "Create a single-zone volume in given zone.")
	cmd.AddCommand(ctrlCmd)

	return cmd
}

func runOperatorWithCredentialsConfig(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	return efscreate.RunOperator(ctx, controllerConfig, useLocalAWSCredentials, singleZone)
}
