package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestFormatBodyPreviewTruncationBoundary: a body exactly at the cap stays
// inline; one character over triggers the truncated form. Locks the
// off-by-one so refactors of BodyPreviewMaxRunes can't silently regress.
func TestFormatBodyPreviewTruncationBoundary(t *testing.T) {
	at := strings.Repeat("x", BodyPreviewMaxRunes)
	over := strings.Repeat("x", BodyPreviewMaxRunes+1)
	if got := FormatBodyPreview(at); strings.Contains(got, "...") {
		t.Errorf("body of exactly %d chars should not be truncated; got %s", BodyPreviewMaxRunes, got)
	}
	if got := FormatBodyPreview(over); !strings.Contains(got, "...") {
		t.Errorf("body of %d chars should be truncated; got %s", BodyPreviewMaxRunes+1, got)
	}
}

// TestFormatBodyPreviewMultibyteSafe: truncation must split on rune
// boundaries so multi-byte UTF-8 sequences never get sliced in half.
func TestFormatBodyPreviewMultibyteSafe(t *testing.T) {
	// 50 Japanese chars (each 3 bytes in UTF-8) → 150 bytes, 50 runes.
	in := strings.Repeat("あ", 50)
	got := FormatBodyPreview(in)
	if !strings.Contains(got, "(50 chars)") {
		t.Errorf("rune count suffix wrong: %s", got)
	}
	// The preview should still be valid UTF-8 (no broken multi-byte sequences).
	if !utf8.ValidString(got) {
		t.Errorf("preview produced invalid UTF-8: %q", got)
	}
}

// TestFormatBodyPreviewEmptyStringRendersAsEmptyQuotes: an empty body is
// the explicit "force empty body" case from the CLI / yml, and must be
// rendered as `''` rather than nothing so users can distinguish "this
// rule clears the body" from "this rule doesn't touch the body".
func TestFormatBodyPreviewEmptyStringRendersAsEmptyQuotes(t *testing.T) {
	if got := FormatBodyPreview(""); got != "''" {
		t.Errorf(`empty body should render as "''", got %q`, got)
	}
}

// TestFormatBodyPreviewEscapesControlChars: newlines / tabs / single
// quotes must never appear literally in the output — they would either
// break the single-line layout of the rules list / TUI, or visually
// conflict with the surrounding `'...'` delimiters.
func TestFormatBodyPreviewEscapesControlChars(t *testing.T) {
	cases := map[string]string{
		"a\nb":  `'a\nb'`,
		"a\rb":  `'a\rb'`,
		"a\tb":  `'a\tb'`,
		`a'b`:   `'a\'b'`,
		`a\b`:   `'a\\b'`,
	}
	for in, want := range cases {
		if got := FormatBodyPreview(in); got != want {
			t.Errorf("FormatBodyPreview(%q) = %q, want %q", in, got, want)
		}
	}
}
