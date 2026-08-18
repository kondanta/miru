package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kondanta/miru/internal/downloader"
	"github.com/spf13/cobra"
)

func cookiesCmd() *cobra.Command {
	var output, dataDir string

	cmd := &cobra.Command{
		Use:   "cookies COOKIE_HEADER",
		Short: "Write YouTube browser cookies to a Netscape-format file",
		Long: `Parse a Cookie header value copied from browser DevTools and write it to a
Netscape-format cookies file for use with MIRU_YTDLP_COOKIES_FILE.

How to get the Cookie header:
  1. Open YouTube in your browser and log in.
  2. Open DevTools → Network tab → reload the page.
  3. Click XHR to filter requests.
  4. Click any youtube.com request → Headers → find the "Cookie:" request header.
  5. Copy everything after "Cookie: " and pass it as the argument to this command.

Example:
  miru cookies "VISITOR_INFO1_LIVE=abc; SID=def; ..."`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dataDir == "" {
				dataDir = os.Getenv("MIRU_DATA_DIR")
			}
			if output == "" {
				if dataDir == "" {
					return fmt.Errorf("--output is required when MIRU_DATA_DIR is not set")
				}
				output = filepath.Join(dataDir, "cookies.txt")
			}

			raw := args[0]
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
