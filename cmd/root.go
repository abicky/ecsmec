package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
)

const version = "0.1.3"

// This variable should be overwritten by -ldflags
var revision = "HEAD"

var rootCmd = &cobra.Command{
	Use:           "ecsmec",
	Short:         "A CLI tool for Amazon ECS that provides some commands to execute bothersome operations",
	Long:          "A CLI tool for Amazon ECS that provides some commands to execute bothersome operations",
	SilenceErrors: true,
	SilenceUsage:  true,
	Version:       version,
}

type runtimeError struct {
	err error
}

func (e *runtimeError) Error() string {
	return fmt.Sprintf("%+v", e.err)
}

func newRuntimeError(format string, a ...any) error {
	return &runtimeError{
		err: xerrors.Errorf(format, a...),
	}
}

func Execute() int {
	if cmd, err := rootCmd.ExecuteC(); err != nil {
		var rerr *runtimeError
		if errors.As(err, &rerr) {
			rootCmd.Println("Error:", err)
			return 1
		}

		rootCmd.Println("Error:", err)
		if cmd != nil {
			rootCmd.Println(cmd.UsageString())
		}
		return 1
	}
	return 0
}

func init() {
	rootCmd.SetVersionTemplate(fmt.Sprintf(
		`{{with .Name}}{{printf "%%s " .}}{{end}}{{printf "version %%s" .Version}} (revision %s)
`, revision))
	rootCmd.PersistentFlags().String("profile", "", "An AWS profile name in your credential file")
	rootCmd.PersistentFlags().String("region", "", "The AWS region")
}

func newConfig(ctx context.Context) (aws.Config, error) {
	region, _ := rootCmd.Flags().GetString("region")
	profile, _ := rootCmd.Flags().GetString("profile")
	return config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithSharedConfigProfile(profile))
}
