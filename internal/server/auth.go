package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const sessionCookieName = "cb_session"
const sessionDuration = 24 * time.Hour

type sessionStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time // session token → expiry
}

func newSessionStore() *sessionStore {
	return &sessionStore{tokens: make(map[string]time.Time)}
}

func (ss *sessionStore) create() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	ss.mu.Lock()
	ss.tokens[token] = time.Now().Add(sessionDuration)
	ss.mu.Unlock()
	return token, nil
}

func (ss *sessionStore) valid(token string) bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	exp, ok := ss.tokens[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(ss.tokens, token)
		return false
	}
	return true
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	hash := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(hash[:])
	ok, err := s.store.ConsumeMagicToken(r.Context(), tokenHash)
	if err != nil || !ok {
		http.Error(w, "Link expired or invalid. Send !login via WhatsApp to get a new one.", http.StatusUnauthorized)
		return
	}
	sessionToken, err := s.sessions.create()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(sessionDuration),
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(loginHTML))
}

const loginHTML = `<!DOCTYPE html>
<html>
<head>
<meta charset="UTF-8">
<title>Claude Bridge — Login</title>
<style>
body{font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;background:#111}
.card{background:#1e1e1e;border:1px solid #333;border-radius:12px;padding:40px;max-width:400px;text-align:center;color:#eee}
h2{margin:0 0 12px;font-size:22px}
p{color:#aaa;font-size:14px;line-height:1.6}
code{background:#333;padding:2px 6px;border-radius:4px;font-family:monospace}
</style>
</head>
<body>
<div class="card">
<h2>Claude Bridge</h2>
<p>Send <code>!login</code> via WhatsApp from your owner number to receive a login link.</p>
</div>
</body>
</html>`

func (s *Server) sessionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		// Allow: auth endpoints, static assets, MCP endpoints, all /api/ routes (used internally)
		if path == "/auth" || path == "/login" || path == "/callback" ||
			len(path) >= 8 && path[:8] == "/static/" ||
			len(path) >= 5 && path[:5] == "/mcp/" ||
			len(path) >= 5 && path[:5] == "/api/" {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !s.sessions.valid(cookie.Value) {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}
