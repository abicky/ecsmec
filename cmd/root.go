package cmd

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/spf13/cobra"
	"golang.org/x/xerrors"
)

const version = "0.1.2"

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

func newRuntimeError(format string, a ...interface{}) error {
	return &runtimeError{
		err: xerrors.Errorf(format, a...),
	}
}

func Execute() int {
	if cmd, err := rootCmd.ExecuteC(); err != nil {
		var rerr *runtimeError
		if xerrors.As(err, &rerr) {
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

func newSession() (*session.Session, error) {
	region, _ := rootCmd.Flags().GetString("region")
	profile, _ := rootCmd.Flags().GetString("profile")
	return session.NewSessionWithOptions(session.Options{
		Config: aws.Config{
			Region: aws.String(region),
		},
		Profile: profile,
	})
}
