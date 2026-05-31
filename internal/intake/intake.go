// Package intake converts a Telegram message with an attachment into plain
// text the dispatcher can treat as if the owner had typed it.
//
// The dispatcher is text-in / text-out. Real-world owners send PDFs, photos,
// CSVs, etc. — this package handles the download + content extraction so the
// LLM never has to know about Telegram's wire formats.
package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"

	"claude-bridge/internal/connectors/telegram"
)

// Downloader is the subset of *telegram.Client this package needs. Tests
// inject a stub.
type Downloader interface {
	FetchAttachment(ctx context.Context, fileID string) ([]byte, *telegram.File, error)
}

// VisionDescriber describes an image. Real impl shells out to `claude --print
// --image`; tests inject a stub.
type VisionDescriber interface {
	DescribeImage(ctx context.Context, path string) (string, error)
}

// Max bytes pulled from any single attachment. PDFs are extracted to text
// before this cap applies; raw bytes above the cap are rejected so a 50MB CSV
// doesn't blow up RAM.
const maxAttachmentBytes = 20 * 1024 * 1024

// maxExtractedChars caps the text fed to the dispatcher so the Claude prompt
// stays under a sane size. Tail is dropped with a marker. 8k is enough for
// routing decisions and a short summary; bigger PDFs blew past the 30-120s
// Reply timeout when Sonnet had to chew through them.
const maxExtractedChars = 8_000

// ExtractMessage inspects m and returns the text the dispatcher should see.
// Returns (text, ok). When ok is false, the caller should reply to the owner
// directly with text (e.g. for unsupported attachments) and NOT invoke the
// dispatcher.
func ExtractMessage(ctx context.Context, dl Downloader, vd VisionDescriber, m *telegram.Message) (string, bool, error) {
	if m == nil {
		return "", false, errors.New("intake: nil message")
	}

	switch {
	case m.Document != nil:
		return extractDocument(ctx, dl, vd, m)
	case len(m.Photo) > 0:
		return extractPhoto(ctx, dl, vd, m)
	case m.Voice != nil || m.Audio != nil:
		return "Audio/voice notes aren't supported yet — please type or send as text.", false, nil
	case m.Video != nil:
		return "Videos aren't supported yet — please share a frame as a photo if needed.", false, nil
	case m.Text != "":
		return m.Text, true, nil
	}
	return "", false, errors.New("intake: empty message")
}

func extractDocument(ctx context.Context, dl Downloader, vd VisionDescriber, m *telegram.Message) (string, bool, error) {
	doc := m.Document
	if doc.FileSize > maxAttachmentBytes {
		return fmt.Sprintf("File %q is too large (%d bytes, limit %d). Please send a smaller version.", doc.FileName, doc.FileSize, maxAttachmentBytes), false, nil
	}

	raw, file, err := dl.FetchAttachment(ctx, doc.FileID)
	if err != nil {
		return "", false, fmt.Errorf("download %s: %w", doc.FileName, err)
	}
	if len(raw) > maxAttachmentBytes {
		return fmt.Sprintf("File %q exceeded the size cap on download.", doc.FileName), false, nil
	}

	name := firstNonEmpty(doc.FileName, filepath.Base(file.FilePath), "attachment")
	kind := classifyDocument(name, doc.MimeType)

	switch kind {
	case kindPDF:
		text, err := extractPDF(raw)
		if err != nil {
			return "", false, fmt.Errorf("pdf %s: %w", name, err)
		}
		return buildDispatcherPrompt("PDF", name, m.Caption, text), true, nil

	case kindText:
		if !utf8.Valid(raw) {
			return fmt.Sprintf("File %q isn't valid UTF-8 text — please send a PDF or image.", name), false, nil
		}
		return buildDispatcherPrompt("text file", name, m.Caption, string(raw)), true, nil

	case kindImage:
		// Telegram sometimes delivers images as "document" rather than "photo"
		// (when sent uncompressed). Run vision the same way as Photo.
		desc, err := describeImageBytes(ctx, vd, raw, name)
		if err != nil {
			return "", false, fmt.Errorf("describe image %s: %w", name, err)
		}
		return buildDispatcherPrompt("image", name, m.Caption, desc), true, nil
	}

	return fmt.Sprintf("File %q (%s) isn't supported yet. Please send PDF, text, or an image.", name, doc.MimeType), false, nil
}

func extractPhoto(ctx context.Context, dl Downloader, vd VisionDescriber, m *telegram.Message) (string, bool, error) {
	best := m.LargestPhoto()
	if best == nil {
		return "", false, errors.New("intake: empty photo array")
	}
	raw, file, err := dl.FetchAttachment(ctx, best.FileID)
	if err != nil {
		return "", false, fmt.Errorf("download photo: %w", err)
	}
	name := firstNonEmpty(filepath.Base(file.FilePath), "photo.jpg")
	desc, err := describeImageBytes(ctx, vd, raw, name)
	if err != nil {
		return "", false, fmt.Errorf("describe photo: %w", err)
	}
	return buildDispatcherPrompt("photo", name, m.Caption, desc), true, nil
}

func describeImageBytes(ctx context.Context, vd VisionDescriber, raw []byte, name string) (string, error) {
	if vd == nil {
		return "(image vision unavailable — Claude CLI not configured)", nil
	}
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		ext = ".jpg"
	}
	f, err := os.CreateTemp("", "tg-intake-*"+ext)
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	return vd.DescribeImage(ctx, path)
}

func extractPDF(raw []byte) (string, error) {
	r, err := pdf.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return "", err
	}
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
		return "(PDF contains no extractable text — it may be a scanned image. Try sending it as a photo for vision.)", nil
	}
	return out, nil
}

type docKind int

const (
	kindUnknown docKind = iota
	kindPDF
	kindText
	kindImage
)

func classifyDocument(name, mime string) docKind {
	ext := strings.ToLower(filepath.Ext(name))
	mime = strings.ToLower(mime)

	switch {
	case ext == ".pdf" || mime == "application/pdf":
		return kindPDF
	case ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".webp" || ext == ".gif",
		strings.HasPrefix(mime, "image/"):
		return kindImage
	case ext == ".txt" || ext == ".md" || ext == ".markdown" || ext == ".csv" || ext == ".json" || ext == ".yaml" || ext == ".yml" || ext == ".log",
		strings.HasPrefix(mime, "text/"),
		mime == "application/json", mime == "application/yaml":
		return kindText
	}
	return kindUnknown
}

func buildDispatcherPrompt(kind, name, caption, body string) string {
	body = strings.TrimSpace(body)
	if utf8.RuneCountInString(body) > maxExtractedChars {
		runes := []rune(body)
		body = string(runes[:maxExtractedChars]) + "\n…[truncated]"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "[Owner attached a %s named %q]", kind, name)
	if caption = strings.TrimSpace(caption); caption != "" {
		sb.WriteString("\nCaption: ")
		sb.WriteString(caption)
	}
	sb.WriteString("\n\n--- Content ---\n")
	sb.WriteString(body)
	return sb.String()
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
