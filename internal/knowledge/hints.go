// Package knowledge implements folder-based document ingestion:
// scan + watch a configured folder, extract text, classify via Claude,
// and index into SQLite FTS5 for retrieval by the MCP tools.
package knowledge

import (
	"path/filepath"
	"strings"

	"claude-bridge/internal/claude"
)

// HintsFromPath derives a best-effort partial metadata set from a file path's
// directory segments. Any field returned non-empty is authoritative and the
// classifier prompt is told to prefer it.
//
// Rules (first match wins):
//
//	doc_type: segment matches "policies"|"policy" → policy
//	          "promo"|"promotions"|"campaign"     → promotion
//	          "templates"|"template"              → template
//	language: segment matches "en"|"english"                 → en
//	          "zh"|"cn"|"chinese"|"mandarin"|"chi"           → zh
//	          "ms"|"malay"|"bahasa"|"bm"                     → ms
//	product:  segment matches "life"     → life
//	          "medical"|"health"         → medical
//	          "motor"|"auto"|"car"       → motor
//	          "travel"                   → travel
//	          "business"|"biz"|"sme"     → business
func HintsFromPath(path string) claude.Hints {
	var h claude.Hints
	segs := splitPathSegments(path)
	for _, raw := range segs {
		s := strings.ToLower(raw)
		if h.DocType == "" {
			switch {
			case s == "policies" || s == "policy":
				h.DocType = "policy"
			case s == "promo" || s == "promos" || s == "promotions" || s == "campaign" || s == "campaigns":
				h.DocType = "promotion"
			case s == "templates" || s == "template":
				h.DocType = "template"
			}
		}
		if h.Language == "" {
			switch s {
			case "en", "english":
				h.Language = "en"
			case "zh", "cn", "chinese", "mandarin", "chi":
				h.Language = "zh"
			case "ms", "malay", "bahasa", "bm":
				h.Language = "ms"
			}
		}
		if h.Product == "" {
			switch s {
			case "life":
				h.Product = "life"
			case "medical", "health":
				h.Product = "medical"
			case "motor", "auto", "car":
				h.Product = "motor"
			case "travel":
				h.Product = "travel"
			case "business", "biz", "sme":
				h.Product = "business"
			}
		}
	}
	return h
}

// splitPathSegments returns the directory components of p, excluding the final
// filename. Works with mixed separators (treats both / and \ as separators so
// that Windows-style paths classify correctly even when the binary runs on Unix).
func splitPathSegments(p string) []string {
	// normalize separators first so filepath.Dir on Unix can strip the filename
	// out of a Windows-style path like "C:\a\b\file.docx".
	norm := strings.ReplaceAll(p, "\\", "/")
	dir := filepath.Dir(norm)
	parts := strings.Split(dir, "/")
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s != "" && s != "." {
			out = append(out, s)
		}
	}
	return out
}
