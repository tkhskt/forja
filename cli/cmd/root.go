// Package cmd defines forja's cobra command tree. The entry point is
// Execute, called from main.go.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Globals carries flag values that apply to multiple subcommands. Cobra's
// PersistentFlags would also work but a struct is easier to pass to engine
// constructors that don't need the cobra Command itself.
type Globals struct {
	BundleDir string // path to JVMTI agent build outputs (overridable via --bundle)
}

var globals = Globals{}

// versionString is stamped by the release pipeline via -ldflags; set from
// main.go through SetVersion. Defaults to "dev" for source builds.
var versionString = "dev"

// SetVersion lets main.go propagate the build-stamped version into cobra's
// --version / `forja --version` output.
func SetVersion(v string) {
	if v != "" {
		versionString = v
	}
}

// NewRoot builds the root cobra command. Exposed so it can be exercised in
// tests without going through main.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "forja",
		Short: "Rewrite OkHttp responses on debuggable Android apps at runtime",
		Long: `forja injects a JVMTI agent into a debuggable Android process and
rewrites OkHttp responses according to rules defined under ./.forja/.

Run 'forja init' once at the project root to set the directory up. The rule
catalog is split across two scopes for shareability:

  .forja/rules.yml         - project scope, committed, shared by the team
  .forja/rules.local.yml   - local scope, personal additions (gitignore it)

Per-rule enabled state is machine-managed and lives in the OS user cache
(~/Library/Caches/forja on macOS, ~/.cache/forja on Linux), keyed by project —
not under .forja/, so there's nothing extra to gitignore.

The on-device copy is consumed and deleted by the agent so nothing persists
across app process restarts.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       versionString,
	}
	root.PersistentFlags().StringVar(&globals.BundleDir, "bundle", "",
		"override agent bundle directory (resolved from FORJA_BUNDLE_DIR, "+
			"XDG_DATA_HOME/forja/agent, ~/.local/share/forja/agent, "+
			"/usr/local/share/forja/agent, or ./jvmti-agent/build/outputs/agent)")

	root.AddCommand(newInitCmd())
	root.AddCommand(newRulesCmd())
	root.AddCommand(newApplyCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newOffCmd())
	root.AddCommand(newAliasCmd())
	return root
}

// Execute is what main.go calls.
func Execute() {
	if err := NewRoot().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "forja: %v\n", err)
		os.Exit(1)
	}
}
