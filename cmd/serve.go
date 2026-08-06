package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// serveCmd creates the command that starts the Miru HTTP server.
func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the miru HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println("hello, miru")
			return nil
		},
	}
}
