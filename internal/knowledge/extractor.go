package knowledge

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/nguyenthenguyen/docx"
)

// ExtractResult is what the extract step returns. For images, Text is empty and
// ImageBytes/MimeType carry the bytes for the vision call.
type ExtractResult struct {
	Text       string
	ImageBytes []byte
	MimeType   string
}

// ErrUnsupportedType is returned when a file extension is not handled.
var ErrUnsupportedType = errors.New("unsupported file type")

// IsSupported reports whether the extractor will handle this file.
func IsSupported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf", ".docx", ".md", ".markdown", ".txt", ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

// MimeTypeFor guesses the mime type from file extension.
func MimeTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	}
	return "application/octet-stream"
}

// Extract opens path and returns extracted text, or image bytes for images.
func Extract(path string) (ExtractResult, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return extractPDF(path)
	case ".docx":
		return extractDOCX(path)
	case ".md", ".markdown", ".txt":
		return extractText(path)
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return extractImage(path)
	}
	return ExtractResult{}, fmt.Errorf("%w: %s", ErrUnsupportedType, ext)
}

func extractText(path string) (ExtractResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ExtractResult{}, err
	}
	return ExtractResult{Text: string(b)}, nil
}

func extractPDF(path string) (ExtractResult, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("pdf open: %w", err)
	}
	defer f.Close()

	var buf bytes.Buffer
	reader, err := r.GetPlainText()
	if err != nil {
		return ExtractResult{}, fmt.Errorf("pdf text: %w", err)
	}
	if _, err := io.Copy(&buf, reader); err != nil {
		return ExtractResult{}, fmt.Errorf("pdf copy: %w", err)
	}
	return ExtractResult{Text: buf.String()}, nil
}

func extractDOCX(path string) (ExtractResult, error) {
	r, err := docx.ReadDocxFile(path)
	if err != nil {
		return ExtractResult{}, fmt.Errorf("docx open: %w", err)
	}
	defer r.Close()
	content := r.Editable().GetContent()
	// nguyenthenguyen/docx returns full WordprocessingML XML; strip tags crudely.
	return ExtractResult{Text: stripXMLTags(content)}, nil
}

func extractImage(path string) (ExtractResult, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ExtractResult{}, err
	}
	return ExtractResult{ImageBytes: b, MimeType: MimeTypeFor(path)}, nil
}

// stripXMLTags removes <...> runs and collapses whitespace. Good enough for
// full-text indexing; not a full DOCX parser.
func stripXMLTags(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
			buf.WriteByte(' ')
		case !inTag:
			buf.WriteRune(r)
		}
	}
	// collapse runs of whitespace
	out := buf.String()
	return collapseWhitespace(out)
}

func collapseWhitespace(s string) string {
	var buf strings.Builder
	buf.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !prevSpace {
				buf.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		buf.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(buf.String())
}
