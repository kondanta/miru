// Package cmd implements the miru command tree.
// All cobra command definitions live here. Business logic lives in internal/.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is the build version, overridable at link time:
//
//	go build -ldflags "-X github.com/kondanta/miru/cmd.version=1.2.3"
var version = "dev"

// Execute builds the command tree and runs it. Called from main.
func Execute() {
	rootCmd := &cobra.Command{
		Use:           "miru",
		Short:         "Self-hosted YouTube downloader with Jellyfin integration",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.Flags().Bool("version", false, "print version and exit")
	rootCmd.RunE = func(cmd *cobra.Command, _ []string) error {
		v, _ := cmd.Flags().GetBool("version")
		if v {
			fmt.Printf("miru %s\n", version)
			return nil
		}
		return cmd.Help()
	}

	rootCmd.AddCommand(serveCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
