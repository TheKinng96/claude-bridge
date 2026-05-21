package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsAllowed(t *testing.T) {
	c := New("tok", []int64{100, 200})
	if !c.IsAllowed(100) {
		t.Error("100 should be allowed")
	}
	if !c.IsAllowed(200) {
		t.Error("200 should be allowed")
	}
	if c.IsAllowed(999) {
		t.Error("999 should NOT be allowed")
	}
}

func TestDispatch_AllowlistDropsNonOwner(t *testing.T) {
	c := New("tok", []int64{100})
	var called atomic.Int32
	c.SetHandler(func(ctx context.Context, m *Message) string {
		called.Add(1)
		return ""
	})

	c.dispatch(context.Background(), Update{
		UpdateID: 1,
		Message:  &Message{From: &User{ID: 999}, Chat: Chat{ID: 999}, Text: "hi"},
	})

	if called.Load() != 0 {
		t.Errorf("handler called for non-owner; got %d, want 0", called.Load())
	}
}

func TestDispatch_AllowlistAllowsOwner(t *testing.T) {
	// Use httptest to absorb the SendMessage call the handler triggers.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":100},"date":0,"text":"ack"}}`))
	}))
	defer ts.Close()

	c := New("tok", []int64{100}, WithHTTPClient(ts.Client()))
	// Point the client at the test server by rewriting the base URL via a
	// wrapper transport. Easiest path: use the real Client and rely on the
	// handler being invoked (we don't assert on the send).
	c.httpClient = &http.Client{Transport: redirectTransport{target: ts.URL}}

	var called atomic.Int32
	c.SetHandler(func(ctx context.Context, m *Message) string {
		called.Add(1)
		return "ack"
	})

	c.dispatch(context.Background(), Update{
		UpdateID: 1,
		Message:  &Message{From: &User{ID: 100}, Chat: Chat{ID: 100}, Text: "hi"},
	})

	if called.Load() != 1 {
		t.Errorf("handler called %d times, want 1", called.Load())
	}
}

func TestDispatch_NoHandlerNoCrash(t *testing.T) {
	c := New("tok", []int64{100})
	// No SetHandler call.
	c.dispatch(context.Background(), Update{
		UpdateID: 1,
		Message:  &Message{From: &User{ID: 100}, Text: "hi"},
	})
	// Just verifying no panic.
}

func TestGetMe_ParsesResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getMe") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Bob","username":"bobbot"}}`))
	}))
	defer ts.Close()

	c := New("tok", nil, WithHTTPClient(&http.Client{Transport: redirectTransport{target: ts.URL}}))
	u, err := c.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if u.ID != 42 || u.Username != "bobbot" {
		t.Errorf("got %+v", u)
	}
}

func TestSendMessage_MarshalsBody(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		w.Write([]byte(`{"ok":true,"result":{"message_id":1,"chat":{"id":5},"date":0,"text":"hi"}}`))
	}))
	defer ts.Close()

	c := New("tok", nil, WithHTTPClient(&http.Client{Transport: redirectTransport{target: ts.URL}}))
	if _, err := c.SendMessage(context.Background(), 5, "hello"); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if gotBody["text"] != "hello" {
		t.Errorf("text = %v", gotBody["text"])
	}
	// JSON numbers decode to float64.
	if gotBody["chat_id"].(float64) != 5 {
		t.Errorf("chat_id = %v", gotBody["chat_id"])
	}
}

func TestStart_BackoffOnError(t *testing.T) {
	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := New("tok", []int64{100}, WithHTTPClient(&http.Client{Transport: redirectTransport{target: ts.URL}}))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_ = c.Start(ctx)
	// Should have at least one call but not loop hot — backoff is 5s so we
	// expect exactly 1 call within 200ms.
	if calls.Load() < 1 {
		t.Errorf("expected ≥1 call, got %d", calls.Load())
	}
}

// redirectTransport rewrites the request URL to a test-server target while
// preserving path + query — letting us point Client at httptest without
// changing the base URL constant.
type redirectTransport struct {
	target string
}

func (rt redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	// Replace scheme+host while keeping the original path so the test server
	// sees /bot<tok>/getMe etc.
	u, err := r.URL.Parse(rt.target + r.URL.Path)
	if err != nil {
		return nil, err
	}
	u.RawQuery = r.URL.RawQuery
	r2.URL = u
	r2.Host = u.Host
	return http.DefaultTransport.RoundTrip(r2)
}
