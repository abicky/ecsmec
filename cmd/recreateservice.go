package cmd

import (
	"encoding/json"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/spf13/cobra"

	"github.com/abicky/ecsmec/internal/service"
)

var recreateServiceCmd *cobra.Command

func init() {
	cmd := &cobra.Command{
		Use:   "recreate-service",
		Short: "Recreate a service with overrides",
		Long: `This command creates a new service from the specified service with overrides,
and after the new service becomes stable, it deletes the old one.
Therefore, as necessary, you have to increase the capacity of the cluster the
service belongs to manually so that it has enough capacity for the new service
to place its tasks.`,
		Example: `  You can change the placement strategy of the service "test" in the default cluster
  by the following command:

    ecsmec recreate-service --service test --overrides '{
      "PlacementStrategy": [
        { "Field": "attribute:ecs.availability-zone", "Type": "spread" },
        { "Field": "CPU", "Type": "binpack" }
      ]
    }'

  In the same way, you can change the name of the service "test" in the default
  cluster like below:

    ecsmec recreate-service --service test --overrides '{
      "ServiceName": "new-name"
    }'
`,
		RunE: recreateService,
	}
	rootCmd.AddCommand(cmd)

	cmd.Flags().String("cluster", "default", "The name of the target `CLUSTER`")

	cmd.Flags().String("service", "", "The name of the target `SERVICE` (required)")
	cmd.MarkFlagRequired("service")

	cmd.Flags().String("overrides", "{}", "An `JSON` to override some fields of the new service")

	recreateServiceCmd = cmd
}

func recreateService(cmd *cobra.Command, args []string) error {
	cluster, _ := recreateServiceCmd.Flags().GetString("cluster")
	serviceName, _ := recreateServiceCmd.Flags().GetString("service")
	overrides, _ := recreateServiceCmd.Flags().GetString("overrides")

	var overrideDef service.Definition
	decoder := json.NewDecoder(strings.NewReader(overrides))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&overrideDef); err != nil {
		return newRuntimeError("failed to parse \"overrides\": %w", err)
	}

	cfg, err := newConfig(cmd.Context())
	if err != nil {
		return newRuntimeError("failed to initialize a session: %w", err)
	}

	if err := service.NewService(ecs.NewFromConfig(cfg)).Recreate(cmd.Context(), cluster, serviceName, overrideDef); err != nil {
		return newRuntimeError("failed to recreate the service: %w", err)
	}
	return nil
}
