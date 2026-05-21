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

func TestPersonalize_ProfileFieldsAppearInPrompt(t *testing.T) {
	fc := &fakeClient{reply: "Hi Alice, hope the policy renewal went smoothly!"}
	p := Personalizer{Claude: fc}
	_, err := p.Generate(context.Background(), Input{
		ContactName:  "Alice",
		BaseTemplate: "Hi {{name}}, our new product is here.",
		Profile: &ProfileSnapshot{
			Role:        "client",
			Language:    "en",
			FamilyNotes: "2 kids: Mia 8, Leo 5",
			Interests:   []string{"family insurance", "savings"},
			LastTopics:  []string{"renewal", "kids' policy"},
			CustomNotes: "VIP",
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"Role: client", "Family: 2 kids", "Interests: family insurance, savings", "Recent topics: renewal", "Notes: VIP"}
	for _, w := range want {
		if !strings.Contains(fc.lastUser, w) {
			t.Errorf("prompt missing %q\nPROMPT:\n%s", w, fc.lastUser)
		}
	}
}

func TestPersonalize_EmptyProfileFieldsOmitted(t *testing.T) {
	fc := &fakeClient{reply: "ok"}
	p := Personalizer{Claude: fc}
	_, _ = p.Generate(context.Background(), Input{
		ContactName:  "Alice",
		BaseTemplate: "Hi {{name}}",
		Profile: &ProfileSnapshot{
			Role: "client",
		},
	})
	if strings.Contains(fc.lastUser, "Family:") {
		t.Errorf("empty family field should be omitted: %s", fc.lastUser)
	}
	if strings.Contains(fc.lastUser, "Interests:") {
		t.Errorf("empty interests field should be omitted: %s", fc.lastUser)
	}
	if !strings.Contains(fc.lastUser, "Role: client") {
		t.Errorf("role field missing: %s", fc.lastUser)
	}
}

func TestPersonalize_NilProfileNoProfileBlock(t *testing.T) {
	fc := &fakeClient{reply: "ok"}
	p := Personalizer{Claude: fc}
	_, _ = p.Generate(context.Background(), Input{
		ContactName:  "Alice",
		BaseTemplate: "Hi {{name}}",
	})
	if strings.Contains(fc.lastUser, "Contact profile:") {
		t.Errorf("nil profile should not produce profile block: %s", fc.lastUser)
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
