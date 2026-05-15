package broadcast

import "testing"

func TestRender_BasicSubstitution(t *testing.T) {
	out := Render("Hi {{name}}!", map[string]string{"name": "Alice"})
	if out != "Hi Alice!" {
		t.Fatalf("got %q, want %q", out, "Hi Alice!")
	}
}

func TestRender_MissingKeyPreserved(t *testing.T) {
	out := Render("Hi {{name}}, your {{thing}} is ready", map[string]string{"name": "Alice"})
	if out != "Hi Alice, your {{thing}} is ready" {
		t.Fatalf("got %q", out)
	}
}

func TestRender_MultipleSameKey(t *testing.T) {
	out := Render("{{name}} said hi to {{name}}", map[string]string{"name": "Bob"})
	if out != "Bob said hi to Bob" {
		t.Fatalf("got %q", out)
	}
}

func TestRender_EmptyVars(t *testing.T) {
	out := Render("hello world", nil)
	if out != "hello world" {
		t.Fatalf("got %q", out)
	}
}

func TestRender_WhitespaceTolerant(t *testing.T) {
	out := Render("Hi {{ name }}!", map[string]string{"name": "Alice"})
	if out != "Hi Alice!" {
		t.Fatalf("got %q", out)
	}
}

func TestRender_EmptyStringValue(t *testing.T) {
	out := Render("Hello {{title}}{{name}}!", map[string]string{"title": "", "name": "Alice"})
	if out != "Hello Alice!" {
		t.Fatalf("got %q", out)
	}
}
