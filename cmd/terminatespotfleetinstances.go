package cmd

import (
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/ecs"
	"github.com/spf13/cobra"

	"github.com/abicky/ecsmec/internal/capacity"
	"github.com/abicky/ecsmec/internal/const/ecsconst"
)

var terminateSpotFleetInstancesCmd *cobra.Command

func init() {
	cmd := &cobra.Command{
		Use:   "terminate-spot-fleet-instances",
		Short: "Terminate spot fleet instances",
		Long: `This command terminates all the container instances safely that belong
to the specified spot fleet request.`,
		RunE: terminateSpotFleetInstances,
	}
	rootCmd.AddCommand(cmd)

	cmd.Flags().String("spot-fleet-request-id", "", "The ID of the target `REQUEST` (required)")
	cmd.MarkFlagRequired("spot-fleet-request-id")

	cmd.Flags().String("cluster", "default", "The name of the target `CLUSTER`")

	cmd.Flags().Int64("batch-size", ecsconst.MaxListableContainerInstances, "The number of instances drained at a once")

	terminateSpotFleetInstancesCmd = cmd
}

func terminateSpotFleetInstances(cmd *cobra.Command, args []string) error {
	id, _ := terminateSpotFleetInstancesCmd.Flags().GetString("spot-fleet-request-id")
	cluster, _ := terminateSpotFleetInstancesCmd.Flags().GetString("cluster")
	batchSize, _ := terminateSpotFleetInstancesCmd.Flags().GetInt64("batch-size")

	sess, err := newSession()
	if err != nil {
		return newRuntimeError("failed to initialize a session: %w", err)
	}

	sfr, err := capacity.NewSpotFleetRequest(id, ec2.New(sess))
	if err != nil {
		return newRuntimeError("failed to initialize a SpotFleetRequest: %w", err)
	}

	drainer, err := capacity.NewDrainer(cluster, batchSize, ecs.New(sess))
	if err != nil {
		return newRuntimeError("failed to initialize a Drainer: %w", err)
	}

	if err := sfr.TerminateAllInstances(drainer); err != nil {
		return newRuntimeError("failed to terminate instances: %w", err)
	}
	return nil
}
