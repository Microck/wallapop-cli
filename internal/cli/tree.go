package cli

import "github.com/spf13/cobra"

// CommandTree returns the whole command tree, wired but never executed, so
// tools outside the package can walk it. The docs generator uses it to build
// the command reference from the binary rather than from a copy that drifts.
func CommandTree(version string) *cobra.Command {
	app := &App{Version: version}
	return app.rootCmd()
}
