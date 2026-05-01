// Package broadcast provides helpers for personalized bulk-send loops on top of internal/batch.
package broadcast

import (
	"regexp"
	"strings"
)

var placeholderRE = regexp.MustCompile(`\{\{\s*[a-zA-Z_][a-zA-Z0-9_]*\s*\}\}`)

// Render replaces {{key}} and {{ key }} placeholders in template using vars.
// Missing keys are left as-is so the original placeholder is visible in the output
// (preferable to silent loss when a sender mistypes a variable).
func Render(template string, vars map[string]string) string {
	if len(vars) == 0 {
		return template
	}
	return placeholderRE.ReplaceAllStringFunc(template, func(match string) string {
		key := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(match, "{{"), "}}"))
		if v, ok := vars[key]; ok {
			return v
		}
		return match
	})
}
