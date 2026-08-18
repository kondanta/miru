package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kondanta/miru/internal/downloader"
	"github.com/spf13/cobra"
)

func cookiesCmd() *cobra.Command {
	var output, dataDir string

	cmd := &cobra.Command{
		Use:   "cookies",
		Short: "Write YouTube browser cookies to a Netscape-format file",
		Long: `Read a Cookie header value from stdin and write it to a Netscape-format
cookies file for use with MIRU_YTDLP_COOKIES_FILE. Reading from stdin
keeps the session cookie out of shell history and process listings.

How to get the Cookie header:
  1. Open YouTube in your browser and log in.
  2. Open DevTools → Network tab → reload the page.
  3. Click any youtube.com request → Headers → find "Cookie:" under request headers.
  4. Copy everything after "Cookie: " and pipe it to this command.

Example:
  echo "VISITOR_INFO1_LIVE=abc; SID=def; ..." | miru cookies`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dataDir == "" {
				dataDir = os.Getenv("MIRU_DATA_DIR")
			}
			if output == "" {
				if dataDir == "" {
					return fmt.Errorf("--output is required when MIRU_DATA_DIR is not set")
				}
				output = filepath.Join(dataDir, "cookies.txt")
			}

			rawBytes, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read cookies from stdin: %w", err)
			}
			raw := strings.TrimRight(string(rawBytes), "\r\n")
			if raw == "" {
				return fmt.Errorf("no cookie data on stdin")
			}

			if err := downloader.WriteNetscapeCookies(raw, ".youtube.com", output); err != nil {
				return err
			}
			fmt.Printf("cookies written to %s\n", output)
			fmt.Printf("set MIRU_YTDLP_COOKIES_FILE=%s\n", output)
			return nil
		},
	}

	cmd.Flags().StringVar(&output, "output", "",
		"output file path (default: <data-dir>/cookies.txt)")
	cmd.Flags().StringVar(&dataDir, "data-dir", "",
		"miru data directory (overrides MIRU_DATA_DIR; used to derive default output path)")

	return cmd
}
