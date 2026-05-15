package broadcast

import (
	"context"
	"strings"
	"testing"
)

type fakeClient struct {
	lastSystem string
	lastUser   string
	reply      string
	err        error
}

func (f *fakeClient) Reply(ctx context.Context, system, user string) (string, error) {
	f.lastSystem = system
	f.lastUser = user
	return f.reply, f.err
}

func TestPersonalize_BuildsPromptWithContext(t *testing.T) {
	fc := &fakeClient{reply: "Hi Alice, hope your week is great!"}
	p := Personalizer{Claude: fc}
	out, err := p.Generate(context.Background(), Input{
		ContactName:    "Alice",
		BaseTemplate:   "Hi {{name}}, new offer in.",
		Instructions:   "Keep tone warm and informal.",
		RecentMessages: []string{"thanks so much!", "yes please send"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if out != "Hi Alice, hope your week is great!" {
		t.Fatalf("got %q", out)
	}
	if !strings.Contains(fc.lastUser, "Alice") {
		t.Fatalf("user prompt missing name: %s", fc.lastUser)
	}
	if !strings.Contains(fc.lastUser, "thanks so much!") {
		t.Fatalf("user prompt missing recent message")
	}
	if !strings.Contains(fc.lastSystem, "warm and informal") {
		t.Fatalf("system prompt missing instructions")
	}
}

func TestPersonalize_FallbackOnError(t *testing.T) {
	fc := &fakeClient{err: context.DeadlineExceeded}
	p := Personalizer{Claude: fc}
	out, err := p.Generate(context.Background(), Input{
		ContactName:  "Alice",
		BaseTemplate: "Hi {{name}}, offer.",
	})
	if err != nil {
		t.Fatalf("expected fallback, got err: %v", err)
	}
	if out != "Hi Alice, offer." {
		t.Fatalf("got %q, want template-rendered fallback", out)
	}
}

func TestPersonalize_TrimsWhitespace(t *testing.T) {
	fc := &fakeClient{reply: "  \nHi Alice!\n  "}
	p := Personalizer{Claude: fc}
	out, _ := p.Generate(context.Background(), Input{
		ContactName:  "Alice",
		BaseTemplate: "Hi {{name}}",
	})
	if out != "Hi Alice!" {
		t.Fatalf("got %q", out)
	}
}
