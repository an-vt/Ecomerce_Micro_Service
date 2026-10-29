package domain

import (
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Slugify makes a URL-safe, lower-case, ASCII slug: accents are folded ("Áo thun" -> "ao-thun"), everything that
// is not a letter or digit becomes a single hyphen.
func Slugify(s string) string {
	s = strings.NewReplacer("đ", "d", "Đ", "D").Replace(s)
	// A transformer carries state between calls and is not safe for concurrent use: build one per call.
	folded, _, err := transform.String(transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC), s)
	if err != nil {
		folded = s
	}
	var b strings.Builder
	hyphen := true // suppresses a leading hyphen
	for _, r := range strings.ToLower(folded) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			hyphen = false
		case !hyphen:
			b.WriteByte('-')
			hyphen = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
