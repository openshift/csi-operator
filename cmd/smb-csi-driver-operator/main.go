package main

import (
	"context"
	"os"

	smb "github.com/openshift/csi-operator/pkg/driver/samba"
	"github.com/openshift/library-go/pkg/controller/controllercmd"
	"github.com/spf13/cobra"
	"k8s.io/component-base/cli"
	"k8s.io/utils/clock"

	"github.com/openshift/csi-operator/pkg/operator"
	"github.com/openshift/csi-operator/pkg/version"
)

func main() {
	command := NewOperatorCommand()
	code := cli.Run(command)
	os.Exit(code)
}

func NewOperatorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "smb-csi-driver-operator",
		Short: "OpenShift CIFS/SMB CSI Driver Operator",
		Run: func(cmd *cobra.Command, args []string) {
			cmd.Help()
			os.Exit(1)
		},
	}

	ctrlCmd := controllercmd.NewControllerCommandConfig(
		"smb-csi-driver-operator",
		version.Get(),
		runCSIDriverOperator,
		clock.RealClock{},
	).NewCommand()

	ctrlCmd.Use = "start"
	ctrlCmd.Short = "Start the CIFS/SMB CSI Driver Operator"

	cmd.AddCommand(ctrlCmd)

	return cmd
}

func runCSIDriverOperator(ctx context.Context, controllerConfig *controllercmd.ControllerContext) error {
	opConfig := smb.GetSambaOperatorConfig()
	return operator.RunOperator(ctx, controllerConfig, "", opConfig)
}
