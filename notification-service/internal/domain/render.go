package domain

import (
	"html"
	"regexp"
	"sort"
	"strings"

	"github.com/an-vt/ecom/platform-go/errs"
)

var placeholder = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_.-]+)\s*\}\}`)

// Placeholders lists the distinct placeholder names of src, sorted.
func Placeholders(src string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range placeholder.FindAllStringSubmatch(src, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	sort.Strings(out)
	return out
}

// Render fills every {{name}} of src with params in a single pass.
//
//   - ALL placeholders are replaced. (The monolith's replacePlaceholder re-applied each key to the original template, so
//     only the last key ever took effect.)
//   - A value is inserted as is and never searched again, so a value that itself contains "{{other}}" cannot pull in
//     another parameter.
//   - A placeholder with no parameter is an error, not a literal "{{token}}" in somebody's inbox.
//   - With escape set (HTML bodies), values are HTML-escaped so user-controlled text cannot inject markup; the
//     template's own markup is untouched.
func Render(src string, params map[string]string, escape bool) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(src, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		v, ok := params[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		if escape {
			return html.EscapeString(v)
		}
		return v
	})
	if len(missing) > 0 {
		sort.Strings(missing)
		return "", errs.InvalidArgument(CodeMissingParam, "no value for placeholder(s): "+strings.Join(missing, ", "))
	}
	return out, nil
}
