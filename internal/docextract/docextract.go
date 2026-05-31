// Package docextract pulls plain text out of common "binary" document formats
// so the cowork and KB readers can return useful content to the dispatcher
// instead of an "[binary file …]" placeholder.
//
// Supported today:
//   - PDF  (via github.com/ledongthuc/pdf, same lib intake uses)
//   - DOCX (Office Open XML — unzip + collect <w:t> text nodes)
//
// Anything else returns ok=false; callers fall back to the placeholder.
package docextract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"

	"github.com/ledongthuc/pdf"
)

// MaxBytes caps the extracted text to keep dispatcher prompts manageable.
// Mirrors intake's 4k cap so behaviour is symmetric between Telegram-uploaded
// and disk-resident files.
const MaxBytes = 4_000

// Extract returns plain text for the file at path if the extension is
// supported. ok=false means "unsupported format — caller should fall back".
func Extract(path string) (text string, ok bool, err error) {
	ext := strings.ToLower(extOf(path))
	switch ext {
	case ".pdf":
		t, err := extractPDF(path)
		if err != nil {
			return "", true, fmt.Errorf("pdf: %w", err)
		}
		return clamp(t), true, nil
	case ".docx":
		t, err := extractDOCX(path)
		if err != nil {
			return "", true, fmt.Errorf("docx: %w", err)
		}
		return clamp(t), true, nil
	}
	return "", false, nil
}

// Supported reports whether Extract handles the file extension. Callers can
// short-circuit without opening the file.
func Supported(path string) bool {
	switch strings.ToLower(extOf(path)) {
	case ".pdf", ".docx":
		return true
	}
	return false
}

func extractPDF(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var buf bytes.Buffer
	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		buf.WriteString(text)
		buf.WriteString("\n")
	}
	out := strings.TrimSpace(buf.String())
	if out == "" {
		return "(PDF contains no extractable text — likely a scanned image.)", nil
	}
	return out, nil
}

// extractDOCX reads /word/document.xml and concatenates the text in every
// <w:t> element. Paragraph breaks come from <w:p>; tab/break nodes are
// rendered as spaces. Tables flatten to row-per-line with cells separated by
// tabs. Headers/footers are ignored — body content is usually what the owner
// cares about.
func extractDOCX(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	var docXML *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			docXML = f
			break
		}
	}
	if docXML == nil {
		return "", fmt.Errorf("missing word/document.xml — not a Word document")
	}
	rc, err := docXML.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}

	var out strings.Builder
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					out.WriteString(s)
				}
			case "tab":
				out.WriteByte('\t')
			case "br":
				out.WriteByte('\n')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "tr":
				out.WriteByte('\n')
			case "tc":
				out.WriteByte('\t')
			}
		}
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "(DOCX contains no extractable text.)", nil
	}
	return text, nil
}

func clamp(s string) string {
	if len(s) <= MaxBytes {
		return s
	}
	return s[:MaxBytes] + "\n…(truncated)"
}

func extOf(path string) string {
	for i := len(path) - 1; i >= 0 && path[i] != '/' && path[i] != '\\'; i-- {
		if path[i] == '.' {
			return path[i:]
		}
	}
	return ""
}

