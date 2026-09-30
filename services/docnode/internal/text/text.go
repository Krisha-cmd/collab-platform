// Package text applies edits to document text.
//
// Documents are stored as UTF-16 code units, the same unit the editor
// (CodeMirror, i.e. JavaScript) uses for positions. That way a position sent
// by the browser can be used directly, without converting between encodings.
package text

import (
	"fmt"
	"unicode/utf16"
)

// Change replaces the text in [From, To) with Insert.
// From and To are UTF-16 offsets into the document the change was made against.
type Change struct {
	From   int
	To     int
	Insert string
}

// Encode converts a Go string (UTF-8) to UTF-16 code units.
func Encode(s string) []uint16 { return utf16.Encode([]rune(s)) }

// Decode converts UTF-16 code units back to a Go string.
func Decode(u []uint16) string { return string(utf16.Decode(u)) }

// Len returns the length of s in UTF-16 code units.
func Len(s string) int { return len(Encode(s)) }

// Apply returns doc with all changes applied. doc itself is not modified.
//
// All changes refer to positions in doc (not to the result of earlier changes
// in the list). They must be sorted by From and must not overlap. Apply
// returns an error, and nothing else, if any change is invalid.
func Apply(doc []uint16, changes []Change) ([]uint16, error) {
	prevEnd := 0
	for i, c := range changes {
		switch {
		case c.From < 0 || c.To < c.From || c.To > len(doc):
			return nil, fmt.Errorf("change %d: range [%d, %d) is outside the document (length %d)",
				i, c.From, c.To, len(doc))
		case c.From < prevEnd:
			return nil, fmt.Errorf("change %d: ranges must be sorted and must not overlap", i)
		case splitsPair(doc, c.From) || splitsPair(doc, c.To):
			return nil, fmt.Errorf("change %d: range [%d, %d) splits a character in half", i, c.From, c.To)
		}
		prevEnd = c.To
	}

	out := make([]uint16, 0, len(doc))
	pos := 0
	for _, c := range changes {
		out = append(out, doc[pos:c.From]...)
		out = append(out, Encode(c.Insert)...)
		pos = c.To
	}
	return append(out, doc[pos:]...), nil
}

// splitsPair reports whether position p falls between the two halves of a
// surrogate pair (characters such as emoji take two UTF-16 code units).
func splitsPair(doc []uint16, p int) bool {
	if p <= 0 || p >= len(doc) {
		return false
	}
	highBefore := doc[p-1] >= 0xD800 && doc[p-1] <= 0xDBFF
	lowAfter := doc[p] >= 0xDC00 && doc[p] <= 0xDFFF
	return highBefore && lowAfter
}