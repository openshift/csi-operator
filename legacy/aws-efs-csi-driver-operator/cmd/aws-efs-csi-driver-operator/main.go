package main

import (
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"

	"github.com/openshift/library-go/pkg/controller/controllercmd"

	"github.com/openshift/aws-efs-csi-driver-operator/pkg/operator"
	"github.com/openshift/aws-efs-csi-driver-operator/pkg/version"
)

func main() {
	command := NewOperatorCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aws-efs-csi-driver-operator",
		Short: "OpenShift AWS EFS CSI Driver Operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
			os.Exit(1)
		},
	}

	ctrlCmd := controllercmd.NewControllerCommandConfig(
		"aws-efs-csi-driver-operator",
		version.Get(),
		operator.RunOperator,
	).NewCommand()
	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Start the AWS EFS CSI Driver Operator"

	cmd.AddCommand(ctrlCmd)

	return cmd
}
