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

// Execute builds and runs the miru command tree.
func Execute() {
	var configPath string

	rootCmd := &cobra.Command{
		Use:           "miru",
		Short:         "Self-hosted YouTube downloader with Jellyfin integration",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.PersistentFlags().StringVar(
		&configPath, "config", "", "path to config file",
	)

	rootCmd.AddCommand(serveCmd(&configPath))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
