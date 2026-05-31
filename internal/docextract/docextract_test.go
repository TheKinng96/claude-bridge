package docextract

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupported(t *testing.T) {
	cases := map[string]bool{
		"a.pdf":   true,
		"a.PDF":   true,
		"a.docx":  true,
		"a.DOCX":  true,
		"a.png":   false,
		"a.txt":   false,
		"a":       false,
		"":        false,
		"x/y.pdf": true,
	}
	for path, want := range cases {
		if got := Supported(path); got != want {
			t.Errorf("Supported(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestExtract_UnsupportedReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.png")
	if err := os.WriteFile(p, []byte{0x89, 0x50}, 0o644); err != nil {
		t.Fatal(err)
	}
	text, ok, err := Extract(p)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ok {
		t.Error("png should be unsupported")
	}
	if text != "" {
		t.Errorf("unsupported should return empty text, got %q", text)
	}
}

func TestExtract_DOCXReadsParagraphs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "doc.docx")
	if err := writeMinimalDOCX(p, "Hello world.", "Second line."); err != nil {
		t.Fatal(err)
	}
	text, ok, err := Extract(p)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !ok {
		t.Fatal("docx should be supported")
	}
	if !strings.Contains(text, "Hello world.") || !strings.Contains(text, "Second line.") {
		t.Errorf("missing paragraphs: %q", text)
	}
}

func TestExtract_DOCXMissingDocumentXML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "broken.docx")
	// Zip with no word/document.xml — should error cleanly, not panic.
	if err := writeZipWithFile(p, "irrelevant.txt", "hi"); err != nil {
		t.Fatal(err)
	}
	_, ok, err := Extract(p)
	if !ok {
		t.Fatal("docx extension should be flagged supported regardless of contents")
	}
	if err == nil {
		t.Error("expected error for docx missing word/document.xml")
	}
}

func TestExtract_ClampLongInput(t *testing.T) {
	// Build a docx whose extracted text exceeds MaxBytes.
	long := strings.Repeat("x", MaxBytes+500)
	dir := t.TempDir()
	p := filepath.Join(dir, "big.docx")
	if err := writeMinimalDOCX(p, long); err != nil {
		t.Fatal(err)
	}
	text, _, err := Extract(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "(truncated)") {
		t.Error("expected truncation marker")
	}
}

// --- test helpers ---

func writeMinimalDOCX(path string, paragraphs ...string) error {
	var doc bytes.Buffer
	doc.WriteString(`<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, p := range paragraphs {
		doc.WriteString("<w:p><w:r><w:t>")
		doc.WriteString(xmlEscape(p))
		doc.WriteString("</w:t></w:r></w:p>")
	}
	doc.WriteString(`</w:body></w:document>`)
	return writeZipWithFile(path, "word/document.xml", doc.String())
}

func writeZipWithFile(path, name, body string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return err
	}
	return zw.Close()
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
