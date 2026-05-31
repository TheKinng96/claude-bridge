package intake

import (
	"context"
	"strings"
	"testing"

	"claude-bridge/internal/connectors/telegram"
)

type fakeDL struct {
	bytes []byte
	file  *telegram.File
	err   error
}

func (f *fakeDL) FetchAttachment(ctx context.Context, fileID string) ([]byte, *telegram.File, error) {
	return f.bytes, f.file, f.err
}

type fakeVD struct {
	desc string
	err  error
}

func (f *fakeVD) DescribeImage(ctx context.Context, path string) (string, error) {
	return f.desc, f.err
}

func TestExtractMessage_PlainText(t *testing.T) {
	m := &telegram.Message{Text: "hello"}
	out, ok, err := ExtractMessage(context.Background(), nil, nil, m)
	if err != nil || !ok || out != "hello" {
		t.Fatalf("got (%q, %v, %v)", out, ok, err)
	}
}

func TestExtractMessage_TextDocument(t *testing.T) {
	dl := &fakeDL{bytes: []byte("col_a,col_b\n1,2\n"), file: &telegram.File{FilePath: "docs/data.csv"}}
	m := &telegram.Message{
		Document: &telegram.Document{FileID: "abc", FileName: "data.csv", MimeType: "text/csv", FileSize: 16},
		Caption:  "look at this",
	}
	out, ok, err := ExtractMessage(context.Background(), dl, nil, m)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if !strings.Contains(out, "data.csv") || !strings.Contains(out, "col_a,col_b") {
		t.Errorf("missing payload: %q", out)
	}
	if !strings.Contains(out, "look at this") {
		t.Errorf("missing caption: %q", out)
	}
}

func TestExtractMessage_Image(t *testing.T) {
	dl := &fakeDL{bytes: []byte{0x89, 0x50, 0x4e, 0x47}, file: &telegram.File{FilePath: "photos/x.jpg"}}
	vd := &fakeVD{desc: "A receipt showing $42 at coffee shop."}
	m := &telegram.Message{
		Photo: []telegram.PhotoSize{
			{FileID: "small", FileSize: 100},
			{FileID: "big", FileSize: 50_000},
		},
		Caption: "receipt",
	}
	out, ok, err := ExtractMessage(context.Background(), dl, vd, m)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if !strings.Contains(out, "A receipt showing $42") {
		t.Errorf("vision desc missing: %q", out)
	}
	if !strings.Contains(out, "receipt") {
		t.Errorf("caption missing: %q", out)
	}
}

func TestExtractMessage_Voice_Unsupported(t *testing.T) {
	m := &telegram.Message{Voice: &telegram.Voice{FileID: "v"}}
	out, ok, _ := ExtractMessage(context.Background(), nil, nil, m)
	if ok {
		t.Fatal("voice should be unsupported (ok=false)")
	}
	if !strings.Contains(strings.ToLower(out), "audio") {
		t.Errorf("expected audio-unsupported reply, got %q", out)
	}
}

func TestExtractMessage_Video_Unsupported(t *testing.T) {
	m := &telegram.Message{Video: &telegram.Video{FileID: "v"}}
	_, ok, _ := ExtractMessage(context.Background(), nil, nil, m)
	if ok {
		t.Fatal("video should be unsupported")
	}
}

func TestExtractMessage_OversizedRejected(t *testing.T) {
	m := &telegram.Message{
		Document: &telegram.Document{FileID: "x", FileName: "huge.csv", FileSize: maxAttachmentBytes + 1},
	}
	out, ok, _ := ExtractMessage(context.Background(), nil, nil, m)
	if ok {
		t.Fatal("oversized should reject")
	}
	if !strings.Contains(out, "too large") {
		t.Errorf("expected size error, got %q", out)
	}
}

func TestExtractMessage_UnknownDocType(t *testing.T) {
	dl := &fakeDL{bytes: []byte{0, 1, 2}, file: &telegram.File{FilePath: "files/a.exe"}}
	m := &telegram.Message{
		Document: &telegram.Document{FileID: "e", FileName: "a.exe", MimeType: "application/octet-stream", FileSize: 3},
	}
	out, ok, _ := ExtractMessage(context.Background(), dl, nil, m)
	if ok {
		t.Fatal("unknown type should not dispatch")
	}
	if !strings.Contains(out, "isn't supported") {
		t.Errorf("expected unsupported msg, got %q", out)
	}
}

func TestClassifyDocument(t *testing.T) {
	cases := []struct {
		name, mime string
		want       docKind
	}{
		{"file.pdf", "", kindPDF},
		{"file.PDF", "", kindPDF},
		{"", "application/pdf", kindPDF},
		{"chart.png", "", kindImage},
		{"img.jpg", "image/jpeg", kindImage},
		{"data.csv", "text/csv", kindText},
		{"notes.md", "", kindText},
		{"weird.xyz", "application/x-binary", kindUnknown},
	}
	for _, c := range cases {
		if got := classifyDocument(c.name, c.mime); got != c.want {
			t.Errorf("classifyDocument(%q,%q) = %d, want %d", c.name, c.mime, got, c.want)
		}
	}
}

func TestBuildDispatcherPrompt_TruncatesLongBody(t *testing.T) {
	body := strings.Repeat("a", maxExtractedChars+10)
	out := buildDispatcherPrompt("text file", "big.txt", "", body)
	if !strings.Contains(out, "[truncated]") {
		t.Error("expected truncation marker")
	}
}

func TestExtractMessage_NonUTF8TextRejected(t *testing.T) {
	dl := &fakeDL{bytes: []byte{0xff, 0xfe, 0xfd}, file: &telegram.File{FilePath: "f.txt"}}
	m := &telegram.Message{
		Document: &telegram.Document{FileID: "x", FileName: "f.txt", MimeType: "text/plain", FileSize: 3},
	}
	out, ok, _ := ExtractMessage(context.Background(), dl, nil, m)
	if ok {
		t.Fatal("non-utf8 text should not dispatch")
	}
	if !strings.Contains(out, "UTF-8") {
		t.Errorf("expected utf8 error, got %q", out)
	}
}
