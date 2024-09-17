package main

import (
	"os"

	"github.com/spf13/cobra"

	"k8s.io/component-base/cli"

	"github.com/openshift/library-go/pkg/controller/controllercmd"

	"github.com/openshift/csi-driver-manila-operator/pkg/operator"
	"github.com/openshift/csi-driver-manila-operator/pkg/version"
)

func main() {
	command := NewOperatorCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "csi-driver-manila-operator",
		Short: "OpenShift Manila CSI Driver Operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
			os.Exit(1)
		},
	}

	ctrlCmd := controllercmd.NewControllerCommandConfig(
		"csi-driver-manila-operator",
		version.Get(),
		operator.RunOperator,
	).NewCommand()
	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Start the Manila CSI Driver Operator"

	cmd.AddCommand(ctrlCmd)

	return cmd
}
