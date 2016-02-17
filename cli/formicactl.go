// Package formicactl implements a command line client for formica. Cobra CLI
// is used as framework.
package cli

import (
	"net/url"

	"github.com/spf13/cobra"

	"github.com/giantswarm/formica/controller"
	"github.com/giantswarm/formica/file-system/real"
	"github.com/giantswarm/formica/file-system/spec"
	"github.com/giantswarm/formica/fleet"
)

var (
	globalFlags struct {
		FleetEndpoint string
	}

	newController controller.Controller
	newFileSystem filesystemspec.FileSystem
	newFleet      fleet.Fleet

	MainCmd = &cobra.Command{
		Use:   "formicactl",
		Short: "orchestrate groups of unit files on Fleet clusters",
		Long:  "orchestrate groups of unit files on Fleet clusters",
		Run:   mainRun,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// This callback is executed after flags are parsed and before any
			// command runs.

			URL, err := url.Parse(globalFlags.FleetEndpoint)
			if err != nil {
				panic(err)
			}

			newFleetConfig := fleet.DefaultConfig()
			newFleetConfig.Endpoint = *URL
			newFleet, err = fleet.NewFleet(newFleetConfig)
			if err != nil {
				panic(err)
			}

			newControllerConfig := controller.DefaultConfig()
			newControllerConfig.Fleet = newFleet
			newController = controller.NewController(newControllerConfig)

			newFileSystem = filesystemreal.NewFileSystem()
		},
	}
)

func init() {
	MainCmd.PersistentFlags().StringVar(&globalFlags.FleetEndpoint, "fleet-endpoint", "unix:///var/run/fleet.sock", "endpoint used to connect to fleet")

	MainCmd.AddCommand(createCmd)
	MainCmd.AddCommand(statusCmd)
}

func mainRun(cmd *cobra.Command, args []string) {
	cmd.Help()
}
