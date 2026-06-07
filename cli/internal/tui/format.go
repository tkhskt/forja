// format.go centralizes the small rendering helpers that the rules TUI and
// the cmd-layer `rules list` both want, so the two surfaces stay visually
// consistent. Body preview was historically only used by `rules list`,
// which left the TUI showing only status + match — sufficient for picking
// a rule by name but not for confirming "is this the rule I think it is?"
// before toggling it on / off.
package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// BodyPreviewMaxRunes is the soft cap on the body preview length. Picked
// so the typical line stays readable even when host / path / status are
// also rendered alongside it. The TUI inherits the same cap.
const BodyPreviewMaxRunes = 30

// FormatBodyPreview renders a body string in a way that's legible at a
// glance — single-quoted so JSON / HTML payloads don't drown in escaped
// double quotes, with `\n` / `\r` / `\t` shown as backslash escapes so the
// output stays on one line. Long bodies are truncated by rune (so multi-
// byte characters never get sliced in half) with a `... (N chars)` suffix.
//
// Callers pre-decide whether to render at all; an empty string is treated
// as the explicit "force empty body" case and renders as `''` so that the
// distinction between "no body override" (caller doesn't invoke this) and
// "empty body" stays visible.
func FormatBodyPreview(s string) string {
	const ellipsis = "..."
	if s == "" {
		return "''"
	}
	runes := []rune(s)
	truncated := false
	if len(runes) > BodyPreviewMaxRunes {
		runes = runes[:BodyPreviewMaxRunes]
		truncated = true
	}
	escaper := strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		"\n", `\n`,
		"\r", `\r`,
		"\t", `\t`,
	)
	out := "'" + escaper.Replace(string(runes)) + "'"
	if truncated {
		out += fmt.Sprintf(" %s (%d chars)", ellipsis, utf8.RuneCountInString(s))
	}
	return out
}
