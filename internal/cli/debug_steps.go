package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Microck/wallapop-cli/internal/output"
	"github.com/Microck/wallapop-cli/internal/wallapop"
	"github.com/spf13/cobra"
)

// debugStepsCmd is a hidden experimental command that posts an arbitrary
// payload to /api/v3/steps and prints the raw response. Used to explore the
// server-driven listing wizard.
func (a *App) debugStepsCmd() *cobra.Command {
	var payloadFile string
	cmd := &cobra.Command{
		Use:   "debug-steps",
		Short: "Post a JSON payload to the steps endpoint and print the raw response",
		Long: `Experimental: walk the server-driven listing wizard.

  wallapop debug-steps '<json>'            POST the JSON to /api/v3/steps
  wallapop debug-steps upload PATH f.jpg [f2.jpg ...]
                                           POST multipart images to PATH`,
		Hidden: true,
		Args:   cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "get" {
				if len(args) != 2 {
					return output.Usagef("debug-steps get requires one API path")
				}
				out, err := a.Client.StepsGetRaw(cmd.Context(), args[1])
				if err != nil {
					return err
				}
				fmt.Fprintf(a.Stdout, "%s\n", string(out))
				return nil
			}
			if len(args) > 0 && args[0] == "upload" {
				if len(args) < 3 {
					return output.Usagef("debug-steps upload requires an API path and at least one image")
				}
				imgs, err := loadUploadImages(args[2:])
				if err != nil {
					return err
				}
				out, err := a.Client.StepsUploadRaw(cmd.Context(), args[1], imgs)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.Stdout, "%s\n", string(out))
				return nil
			}
			var payload json.RawMessage
			if payloadFile != "" {
				raw, err := os.ReadFile(payloadFile)
				if err != nil {
					return err
				}
				if err := json.Unmarshal(raw, &payload); err != nil {
					return fmt.Errorf("bad payload JSON: %w", err)
				}
			} else if len(args) > 0 {
				if err := json.Unmarshal([]byte(args[0]), &payload); err != nil {
					return fmt.Errorf("bad payload JSON: %w", err)
				}
			} else {
				payload = json.RawMessage(`{}`)
			}
			out, err := a.Client.StepsRaw(cmd.Context(), payload)
			if err != nil {
				if we, ok := err.(*wallapop.Error); ok && we.Body != "" {
					fmt.Fprintf(a.Stderr, "HTTP %d body: %s\n", we.Status, we.Body)
				}
				return err
			}
			fmt.Fprintln(a.Stdout, string(out))
			return nil
		},
	}
	cmd.Flags().StringVar(&payloadFile, "payload-file", "", "read the JSON payload from a file")
	return cmd
}

func loadUploadImages(paths []string) ([]wallapop.UploadImage, error) {
	var imgs []wallapop.UploadImage
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		ct := "image/jpeg"
		if strings.HasSuffix(strings.ToLower(p), ".png") {
			ct = "image/png"
		}
		imgs = append(imgs, wallapop.UploadImage{Name: filepath.Base(p), Data: data, ContentType: ct})
	}
	return imgs, nil
}
