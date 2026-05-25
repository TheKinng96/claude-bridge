package cowork

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixedRoot(t *testing.T, now time.Time) *Root {
	t.Helper()
	vault := t.TempDir()
	r := New(vault)
	r.Now = func() time.Time { return now }
	return r
}

func mustWrite(t *testing.T, r *Root, date time.Time, name, body string) string {
	t.Helper()
	dir := r.DateFolder(date)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

func TestEnabled(t *testing.T) {
	if New("").Enabled() {
		t.Fatal("empty vault should be disabled")
	}
	if !New("/tmp/x").Enabled() {
		t.Fatal("non-empty vault should be enabled")
	}
}

func TestResolveDate(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	cases := []struct {
		in   string
		want string
	}{
		{"", "2026-05-19"},
		{"today", "2026-05-19"},
		{"yesterday", "2026-05-18"},
		{"2026-04-01", "2026-04-01"},
	}
	for _, c := range cases {
		got, err := r.ResolveDate(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got.Format("2006-01-02") != c.want {
			t.Fatalf("%s: got %s want %s", c.in, got.Format("2006-01-02"), c.want)
		}
	}
	if _, err := r.ResolveDate("not-a-date"); err == nil {
		t.Fatal("expected error for bad date")
	}
}

func TestListEmpty(t *testing.T) {
	r := fixedRoot(t, time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC))
	rows, err := r.List("today")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows, got %d", len(rows))
	}
}

func TestListNewestFirst(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	older := mustWrite(t, r, now, "a.md", "older")
	time.Sleep(10 * time.Millisecond)
	newer := mustWrite(t, r, now, "b.md", "newer")
	// touch newer to make sure ModTime is later
	_ = os.Chtimes(newer, now, now.Add(time.Minute))
	_ = os.Chtimes(older, now, now.Add(-time.Minute))

	rows, err := r.List("today")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0].Name != "b.md" {
		t.Fatalf("expected b.md first, got %s", rows[0].Name)
	}
	if !rows[0].IsText {
		t.Fatal("expected .md IsText=true")
	}
}

func TestReadBinaryFile(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	mustWrite(t, r, now, "cover.png", "\x89PNG\r\n\x1a\n")
	body, entry, err := r.Read("cover.png")
	if err != nil {
		t.Fatal(err)
	}
	if entry.IsText {
		t.Fatal("png should not be text")
	}
	if !strings.Contains(body, "binary file cover.png") {
		t.Fatalf("expected binary placeholder, got %q", body)
	}
}

func TestReadTruncation(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	big := strings.Repeat("x", MaxReadBytes+500)
	mustWrite(t, r, now, "big.md", big)
	body, _, err := r.Read("big.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(body, "…(truncated)") {
		t.Fatal("expected truncation marker")
	}
	if len(body) > MaxReadBytes+50 {
		t.Fatalf("body too long: %d", len(body))
	}
}

func TestFindDatedPath(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	mustWrite(t, r, now, "draft.md", "today")
	e, err := r.Find("2026-05-19/draft.md")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "draft.md" || e.Date != "2026-05-19" {
		t.Fatalf("unexpected entry: %+v", e)
	}
}

func TestFindStripsCoworkPrefix(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	mustWrite(t, r, now, "draft.md", "today")
	if _, err := r.Find("Cowork/2026-05-19/draft.md"); err != nil {
		t.Fatalf("Cowork/ prefix should be stripped: %v", err)
	}
}

func TestFindFuzzyAcrossDates(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	yesterday := now.AddDate(0, 0, -1)
	yPath := mustWrite(t, r, yesterday, "broadcast_tan_120000.md", "older")
	todayPath := mustWrite(t, r, now, "broadcast_tan_140000.md", "newer")
	// Force ModTimes — fs uses wall-clock, but fake now is in the past, so
	// without override yesterday and today would both stamp to real-now.
	_ = os.Chtimes(yPath, yesterday, yesterday)
	_ = os.Chtimes(todayPath, now, now)

	e, err := r.Find("tan")
	if err != nil {
		t.Fatal(err)
	}
	if e.Date != "2026-05-19" {
		t.Fatalf("expected newer date 2026-05-19, got %s (%s)", e.Date, e.Name)
	}
}

func TestFindExactWins(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	yesterday := now.AddDate(0, 0, -1)
	mustWrite(t, r, yesterday, "exact.md", "older")
	mustWrite(t, r, now, "exactly_named.md", "newer fuzzy")

	e, err := r.Find("exact.md")
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "exact.md" {
		t.Fatalf("expected exact.md, got %s", e.Name)
	}
}

func TestFindNotFound(t *testing.T) {
	r := fixedRoot(t, time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC))
	if _, err := r.Find("nope.md"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSearch(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	mustWrite(t, r, now, "draft.md", "Hi Tan WS\nThanks for trusting Etiqa\n")
	mustWrite(t, r, now.AddDate(0, 0, -2), "old.md", "Tan called yesterday")
	mustWrite(t, r, now, "image.png", "not text — should be skipped")

	hits, err := r.Search("tan", 0) // default 7 days
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d: %+v", len(hits), hits)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	r := fixedRoot(t, time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC))
	if _, err := r.Search("   ", 7); err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestEditAppend(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	mustWrite(t, r, now, "draft.md", "original")

	if _, err := r.Edit("draft.md", "P.S. extra", OpAppend); err != nil {
		t.Fatal(err)
	}
	got, _, err := r.Read("draft.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "original") || !strings.Contains(got, "P.S. extra") {
		t.Fatalf("append failed: %q", got)
	}
}

func TestEditReplace(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	mustWrite(t, r, now, "draft.md", "first")
	if _, err := r.Edit("draft.md", "second", OpReplace); err != nil {
		t.Fatal(err)
	}
	got, _, _ := r.Read("draft.md")
	if got != "second" {
		t.Fatalf("expected 'second', got %q", got)
	}
}

func TestEditBinaryRejected(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	mustWrite(t, r, now, "cover.png", "binary")
	if _, err := r.Edit("cover.png", "nope", OpAppend); err == nil {
		t.Fatal("expected binary edit to fail")
	}
}

func TestWriteOutputCreatesDateFolder(t *testing.T) {
	now := time.Date(2026, 5, 19, 14, 30, 15, 0, time.UTC)
	r := fixedRoot(t, now)
	path, err := r.WriteOutput("broadcast", "tan WS!", ".md", []byte("draft body"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "2026-05-19") {
		t.Fatalf("path missing date: %s", path)
	}
	if !strings.Contains(filepath.Base(path), "broadcast_tan-WS_143015.md") {
		t.Fatalf("unexpected filename: %s", filepath.Base(path))
	}
	data, _ := os.ReadFile(path)
	if string(data) != "draft body" {
		t.Fatalf("body mismatch: %q", data)
	}
}

func TestDisabledRoot(t *testing.T) {
	r := New("")
	if _, err := r.List("today"); err == nil {
		t.Fatal("expected disabled error")
	}
	if _, _, err := r.Read("x.md"); err == nil {
		t.Fatal("expected disabled error")
	}
	if _, err := r.Search("x", 7); err == nil {
		t.Fatal("expected disabled error")
	}
	if _, err := r.Edit("x.md", "", OpAppend); err == nil {
		t.Fatal("expected disabled error")
	}
	if _, err := r.Create("today", "x.md", ""); err == nil {
		t.Fatal("expected disabled error")
	}
}

func TestCreate(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)

	entry, err := r.Create("today", "notes_tan.md", "hello")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entry.Name != "notes_tan.md" || entry.Date != "2026-05-19" || !entry.IsText {
		t.Errorf("unexpected entry %+v", entry)
	}
	got, _ := os.ReadFile(entry.Path)
	if string(got) != "hello" {
		t.Errorf("content = %q, want %q", got, "hello")
	}
}

func TestCreate_DefaultsExtensionAndDate(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)

	entry, err := r.Create("", "scratch", "") // no ext, empty date, empty body
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if entry.Name != "scratch.md" {
		t.Errorf("name = %q, want scratch.md", entry.Name)
	}
	if entry.Date != "2026-05-19" {
		t.Errorf("date = %q, want today", entry.Date)
	}
}

func TestCreate_FailsWhenExists(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	mustWrite(t, r, now, "draft.md", "old")

	if _, err := r.Create("today", "draft.md", "new"); err == nil {
		t.Fatal("expected error creating existing file")
	}
	if got, _ := os.ReadFile(filepath.Join(r.DateFolder(now), "draft.md")); string(got) != "old" {
		t.Errorf("existing file clobbered: %q", got)
	}
}

func TestCreate_RejectsPathSeparators(t *testing.T) {
	now := time.Date(2026, 5, 19, 10, 0, 0, 0, time.UTC)
	r := fixedRoot(t, now)
	for _, bad := range []string{"../escape.md", "sub/dir.md", ""} {
		if _, err := r.Create("today", bad, "x"); err == nil {
			t.Errorf("expected error for filename %q", bad)
		}
	}
}
