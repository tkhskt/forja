package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tkhskt/forja/internal/config"
)

// initialRulesYml is the template forja init writes to .forja/rules.yml. The
// comments document the rule schema right where users will look first
// (their editor on the just-created file). Once `forja rules add` writes to
// the file, yaml.v3's encoder will strip these comments — that's fine, by
// then the user has rules of their own to read for reference.
//
// The `rules:` key appears only inside the commented example, never as a
// live key: the file is intentionally comment-only on init. yaml.v3 parses a
// comment-only document into a zero-value RulesFile (Rules == nil), and the
// first `rules add` appends to that nil slice and materializes the real
// `rules:` key naturally on save.
const initialRulesYml = `# forja rule catalog. Hand-editable.
#
# Rules live under a top-level 'rules:' list. Each rule has a unique 'name',
# an optional 'match', and a 'response':
#
# rules:
#   - name: example-mock
#     match:
#       host: example.com           # exact host match
#       path: /api/v2/widgets       # substring of encoded path
#     response:
#       status: 200
#       body: '{"ok":true}'          # inline body (any string scalar)
#       # bodyFile: responses/x.json # alternative: external file
#       # headers:                   # optional response header overrides
#       #   Content-Type: text/html; charset=utf-8
#
# Schema reference:
#   https://github.com/tkhskt/forja/blob/main/docs/usage.md#rule-schema
`

// recommendedGitignoreEntries lists the files inside .forja/ that should be
// kept out of version control. init prints these as a suggestion instead of
// editing .gitignore directly — VCS hygiene is the user's call, not the
// tool's (this is the convention followed by ESLint / Prettier / terraform /
// tsc; project scaffolders like cargo new / gradle init do create one, but
// forja is a config tool layered onto an existing project, not a scaffolder).
//
// status.json is intentionally absent: it no longer lives under .forja/ (it's
// machine-managed cache state, kept in the user cache keyed by project), so
// there's nothing to gitignore for it.
var recommendedGitignoreEntries = []string{
	config.DefaultLocalPath,
	config.DefaultAliasesPath,
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create .forja/ in the current directory (a one-time setup step)",
		Long: `Create a fresh .forja/ directory and seed .forja/rules.yml with a
schema-commented template ready for the first 'forja rules add'.

forja never auto-creates its working directory. Run this once at the project
root before any other command. Subsequent commands refuse to run if .forja/
is missing, so accidentally invoking forja from the wrong cwd no longer
silently spawns an orphan .forja/ directory there.

init does NOT edit your .gitignore — it just prints the recommended entries
afterwards so you can add them by hand. This matches how other config tools
behave (ESLint, terraform, tsc).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit()
		},
	}
}

// runInit performs the directory + rules.yml creation, then prints the
// recommended .gitignore lines so the user can copy them in if they want.
// Errors loudly if .forja/ is already populated — silently overwriting a
// rule catalog would be a footgun.
func runInit() error {
	const dir = config.DefaultDir
	const file = config.DefaultPath

	if _, err := os.Stat(file); err == nil {
		return fmt.Errorf("forja is already initialized here (%s exists). Remove the file first if you really want to reset", file)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", file, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(file, []byte(initialRulesYml), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	fmt.Printf("initialized %s\n", file)
	fmt.Println()
	fmt.Println("You may want to add these lines to .gitignore:")
	for _, e := range recommendedGitignoreEntries {
		fmt.Printf("  %s\n", e)
	}
	fmt.Println()
	fmt.Println("next: forja rules add NAME --host ... --status ...")
	return nil
}

// requireForjaDir is the preflight check every non-init command runs. It
// catches two distinct failure modes loudly instead of letting them turn
// into silent behaviors:
//
//  1. user ran forja before initializing (= run forja init)
//  2. user ran forja from the wrong cwd (= chdir, or init here too)
//
// The error message names both so users don't have to guess.
func requireForjaDir() error {
	info, err := os.Stat(config.DefaultDir)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no .forja/ directory in current working directory.\n" +
				"  run `forja init` here to set one up, or chdir to a forja-managed directory.")
		}
		return fmt.Errorf("stat .forja/: %w", err)
	}
	if !info.IsDir() {
		return errors.New("`.forja` exists in the current working directory but is not a directory")
	}
	return nil
}
