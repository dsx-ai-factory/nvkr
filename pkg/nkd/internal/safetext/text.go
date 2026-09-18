// Package safetext owns the shared bounded text predicate for authored and
// canonical identity fields that must remain unambiguous in logs and reviews.
package safetext

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Valid reports whether value is bounded UTF-8 without display-control runes.
// Ordinary ASCII spaces remain valid; Unicode format characters and non-ASCII
// separators are rejected because they can reorder, hide, or split displayed
// identity text without changing its bytes.
func Valid(value string, maxBytes int, allowEmpty bool) bool {
	return (allowEmpty || value != "") && len(value) <= maxBytes && utf8.ValidString(value) &&
		strings.IndexFunc(value, displayUnsafe) == -1
}

func displayUnsafe(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) ||
		(r != ' ' && unicode.In(r, unicode.Zs))
}
