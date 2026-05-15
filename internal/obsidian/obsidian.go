// Package obsidian writes per-client and per-topic markdown files into an
// Obsidian vault so the user can see their CRM data in Obsidian's graph and
// dataview views. The writer is one-way — it overwrites files outside the
// "## Custom Notes" section on each profile change. Manual edits should go in
// Custom Notes via the dashboard or the dispatch `update_profile` action.
package obsidian

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"claude-bridge/internal/store"
)

// Writer renders profiles into Obsidian vault files. Zero-value Writer is a
// no-op; callers can wire Writer{} when no vault is configured without nil
// checks at every callsite.
type Writer struct {
	VaultPath string
}

// New returns a Writer for the given vault path. An empty vaultPath disables
// all writes (Enabled returns false).
func New(vaultPath string) *Writer { return &Writer{VaultPath: vaultPath} }

// Enabled reports whether the writer has a vault path configured.
func (w *Writer) Enabled() bool { return w != nil && w.VaultPath != "" }

// WriteClient writes (or overwrites) the per-client markdown file. The file
// lives at <vault>/Clients/<name>.md. Returns nil immediately when disabled.
func (w *Writer) WriteClient(p *store.ClientProfile) error {
	if !w.Enabled() || p == nil {
		return nil
	}
	name := safeFilename(p.DisplayName)
	if name == "" {
		name = safeFilename(shortJID(p.JID))
	}
	if name == "" {
		return fmt.Errorf("obsidian: cannot derive filename for profile %q", p.JID)
	}
	dir := filepath.Join(w.VaultPath, "Clients")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("obsidian: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, name+".md")
	content := renderClient(p)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("obsidian: write %s: %w", path, err)
	}
	return nil
}

// WriteTopicStubs creates a stub file under <vault>/Topics/<name>.md for each
// supplied topic that doesn't yet exist. Existing files are left untouched so
// users can add their own notes without losing them.
func (w *Writer) WriteTopicStubs(topics []string) error {
	if !w.Enabled() || len(topics) == 0 {
		return nil
	}
	dir := filepath.Join(w.VaultPath, "Topics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("obsidian: mkdir %s: %w", dir, err)
	}
	for _, t := range topics {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		name := safeFilename(t)
		if name == "" {
			continue
		}
		path := filepath.Join(dir, name+".md")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		stub := "# " + t + "\n\n> Auto-generated stub. Add your notes here.\n\n## Related Clients\n\n## Notes\n"
		if err := os.WriteFile(path, []byte(stub), 0o644); err != nil {
			return fmt.Errorf("obsidian: write %s: %w", path, err)
		}
	}
	return nil
}

// renderClient builds the full markdown content for one client profile.
// Frontmatter is valid YAML; the body uses wikilinks for Obsidian graph view.
func renderClient(p *store.ClientProfile) string {
	var b strings.Builder

	// Frontmatter
	b.WriteString("---\n")
	fmt.Fprintf(&b, "jid: %s\n", yamlEscape(p.JID))
	if p.Role != "" {
		fmt.Fprintf(&b, "role: %s\n", yamlEscape(p.Role))
	}
	if p.Language != "" {
		fmt.Fprintf(&b, "language: %s\n", yamlEscape(p.Language))
	}
	if len(p.Aliases) > 0 {
		fmt.Fprintf(&b, "aliases: [%s]\n", joinYAML(p.Aliases))
	}
	if len(p.Interests) > 0 {
		fmt.Fprintf(&b, "interests: [%s]\n", joinYAML(p.Interests))
	}
	fmt.Fprintf(&b, "last_updated: %s\n", time.Now().Format("2006-01-02"))
	b.WriteString("---\n\n")

	b.WriteString("> Auto-generated. Edits outside \"## Custom Notes\" are overwritten on next profile update.\n\n")

	name := p.DisplayName
	if name == "" {
		name = shortJID(p.JID)
	}
	fmt.Fprintf(&b, "# %s\n\n", name)

	if p.FamilyNotes != "" {
		b.WriteString("## Family\n")
		b.WriteString(p.FamilyNotes)
		b.WriteString("\n\n")
	}

	if len(p.LastTopics) > 0 {
		b.WriteString("## Recent Topics\n")
		for _, t := range p.LastTopics {
			fmt.Fprintf(&b, "- [[%s]]\n", t)
		}
		b.WriteString("\n")
	}

	if len(p.Interests) > 0 {
		b.WriteString("## Interests\n")
		for _, i := range p.Interests {
			fmt.Fprintf(&b, "- %s\n", i)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Custom Notes\n")
	if p.CustomNotes != "" {
		b.WriteString(p.CustomNotes)
		b.WriteString("\n")
	}

	return b.String()
}

// safeFilename strips path separators and disallowed-on-most-filesystems
// characters, then trims surrounding whitespace and dots.
var unsafeFilenameRE = regexp.MustCompile(`[/\\:*?"<>|]`)

func safeFilename(s string) string {
	s = strings.TrimSpace(s)
	s = unsafeFilenameRE.ReplaceAllString(s, "_")
	s = strings.Trim(s, ". ")
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}

// shortJID strips the WhatsApp domain suffix (@s.whatsapp.net).
var jidSuffixRE = regexp.MustCompile(`@[^@]+$`)

func shortJID(j string) string { return jidSuffixRE.ReplaceAllString(j, "") }

// yamlEscape wraps a value in double quotes if it contains characters that
// would break inline YAML (`: `, `#`, leading/trailing whitespace, etc).
func yamlEscape(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, ":#\"\n") || strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// joinYAML formats a slice as a comma-separated inline YAML list. Each item is
// escaped individually.
func joinYAML(items []string) string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = yamlEscape(s)
	}
	return strings.Join(out, ", ")
}
