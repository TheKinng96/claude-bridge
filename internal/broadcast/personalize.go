package broadcast

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// claudeReplier is the subset of *claude.Client we use; defined as an interface
// so tests can inject a fake without depending on the concrete client type.
type claudeReplier interface {
	Reply(ctx context.Context, systemPrompt, conversation string) (string, error)
}

// Input describes one contact's data for personalization.
type Input struct {
	ContactName    string   // used for both {{name}} and {{push_name}} substitution; expose a separate PushName field here if callers ever need to distinguish.
	BaseTemplate   string   // template with {{name}} etc. — also the fallback when Claude fails.
	Instructions   string   // user-supplied tone/style guidance; empty = default instructions.
	RecentMessages []string // most-recent inbound messages from this contact, oldest first.
}

// Personalizer wraps Claude to generate one tailored message per contact.
type Personalizer struct {
	Claude claudeReplier
}

const defaultInstructions = "Rephrase the base message naturally for this specific contact. Keep the meaning. One short message. No greetings beyond a name. No emoji unless the original had one. Reply with ONLY the final message text — no preamble, no quotes."

// Generate returns a personalized message. If Claude fails, returns the rendered
// base template as a fallback so a send never fails because personalization failed.
func (p Personalizer) Generate(ctx context.Context, in Input) (string, error) {
	rendered := Render(in.BaseTemplate, map[string]string{
		"name":      in.ContactName,
		"push_name": in.ContactName,
	})

	if p.Claude == nil {
		return rendered, nil
	}

	instr := in.Instructions
	if instr == "" {
		instr = defaultInstructions
	}

	system := "You write personalized WhatsApp messages. " + instr
	user := buildUserPrompt(in, rendered)

	out, err := p.Claude.Reply(ctx, system, user)
	if err != nil {
		log.Printf("[personalize] Claude error for %q, using template fallback: %v", in.ContactName, err)
		return rendered, nil
	}
	out = strings.TrimSpace(out)
	if out == "" {
		log.Printf("[personalize] Claude returned empty reply for %q, using template fallback", in.ContactName)
		return rendered, nil
	}
	return out, nil
}

func buildUserPrompt(in Input, rendered string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Contact name: %s\n\n", in.ContactName)
	if len(in.RecentMessages) > 0 {
		b.WriteString("Recent messages from this contact (oldest first):\n")
		for _, m := range in.RecentMessages {
			fmt.Fprintf(&b, "  - %s\n", m)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Base message:\n%s\n", rendered)
	return b.String()
}
