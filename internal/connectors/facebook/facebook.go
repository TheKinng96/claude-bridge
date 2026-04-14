// Package facebook provides a browser-based Facebook connector.
// It automates login, posting to Pages, and reading/sending Messenger messages
// using go-rod browser automation via the shared browser engine.
package facebook

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"claude-bridge/internal/browser"
	"claude-bridge/internal/store"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

const (
	platformName    = "facebook"
	loginURL        = "https://www.facebook.com/login"
	messengerURL    = "https://www.messenger.com"
	facebookBaseURL = "https://www.facebook.com"
	loginTimeout    = 30 * time.Second
	actionTimeout   = 20 * time.Second
	healthTimeout   = 15 * time.Second
)

// PageInfo holds information about a connected Facebook Page.
type PageInfo struct {
	PageID   string `json:"page_id"`
	PageName string `json:"page_name"`
	PageURL  string `json:"page_url"`
}

// Connector manages Facebook: browser automation for posting, Graph API for Messenger.
type Connector struct {
	mu     sync.RWMutex
	engine *browser.Engine
	store  *store.Store
	logger *log.Logger

	loggedIn    bool
	userName    string
	profilePic  string
	pageInfo    *PageInfo
	loginStatus LoginStatus
	loginPage   *rod.Page    // the headed browser page during login flow
	loginDone   chan struct{} // closed when watchForLogin finishes cleanup

	// Messenger API (Graph API based)
	Messenger *MessengerAPI
}

// New creates a new Facebook connector.
func New(engine *browser.Engine, appStore *store.Store) *Connector {
	return &Connector{
		engine:    engine,
		store:     appStore,
		logger:    log.Default(),
		Messenger: NewMessengerAPI(appStore),
	}
}

// Boot loads saved OAuth config, restores Facebook session, and starts polling if connected.
func (c *Connector) Boot(ctx context.Context) {
	// Try to load saved profile data from DB first (avoids needing browser on boot)
	accounts, err := c.store.GetAccounts(ctx, platformName)
	if err == nil {
		for _, a := range accounts {
			if a.Status == "connected" && a.PushName != "" {
				c.mu.Lock()
				c.userName = a.PushName
				c.profilePic = a.ProfilePic
				c.mu.Unlock()
				c.logger.Printf("[facebook] Loaded profile from DB: %s", a.PushName)
				break
			}
		}
	}

	// Try to restore Facebook browser session from saved cookies
	if c.RestoreSession(ctx) {
		c.logger.Printf("[facebook] Browser session restored")
	}

	if err := c.Messenger.LoadOAuthConfig(ctx); err != nil {
		c.logger.Printf("[facebook] Warning: failed to load OAuth config: %v", err)
	}
	if c.Messenger.IsConnected() {
		pt := c.Messenger.GetPageInfo()
		c.logger.Printf("[facebook] Messenger connected to page: %s", pt.PageName)
		c.Messenger.StartPolling(ctx)
	}
}

// IsConnected returns whether Facebook is logged in.
func (c *Connector) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loggedIn
}

// UserName returns the logged-in user's name.
func (c *Connector) UserName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.userName
}

// ProfilePic returns the logged-in user's profile picture URL.
func (c *Connector) ProfilePic() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.profilePic
}

// ConnStatus holds all connection status fields, read under a single lock.
type ConnStatus struct {
	Connected  bool   `json:"connected"`
	UserName   string `json:"user_name"`
	ProfilePic string `json:"profile_pic"`
	PageName   string `json:"page_name"`
}

// Status returns all connection status fields atomically (single lock).
func (c *Connector) Status() ConnStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pageName := ""
	if c.pageInfo != nil {
		pageName = c.pageInfo.PageName
	}
	return ConnStatus{
		Connected:  c.loggedIn,
		UserName:   c.userName,
		ProfilePic: c.profilePic,
		PageName:   pageName,
	}
}

// PageName returns the connected page name if any.
func (c *Connector) PageName() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.pageInfo != nil {
		return c.pageInfo.PageName
	}
	return ""
}

// GetPageInfo returns the connected page info.
func (c *Connector) GetPageInfo() *PageInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pageInfo
}

// LoginStatus tracks the state of the interactive login flow.
type LoginStatus struct {
	State   string `json:"state"`   // "idle", "waiting", "logged_in", "error"
	Message string `json:"message,omitempty"`
}

// OpenLoginBrowser opens a visible browser to Facebook so the user can log in.
// The app watches for login completion, then saves the session cookies.
// This runs in the background — poll LoginState() to check progress.
func (c *Connector) OpenLoginBrowser(ctx context.Context) error {
	c.mu.Lock()
	c.loginStatus = LoginStatus{State: "opening", Message: "Opening browser..."}
	c.mu.Unlock()

	go c.watchForLogin(ctx)
	return nil
}

// ConfirmLogin is called when the user clicks "I have logged in".
// It verifies the login, saves cookies, and signals the headed browser to close.
// Profile extraction happens in a separate headless browser session in the background.
func (c *Connector) ConfirmLogin(ctx context.Context) (ok bool, message string) {
	c.mu.RLock()
	page := c.loginPage
	alreadyLoggedIn := c.loggedIn
	c.mu.RUnlock()

	// If auto-detect already verified the login, return success immediately
	if alreadyLoggedIn {
		c.logger.Printf("[facebook] ConfirmLogin: already logged in (auto-detected)")
		name := c.UserName()
		if name == "" || name == "Facebook User" {
			return true, "Login verified! Loading your profile..."
		}
		return true, fmt.Sprintf("Welcome, %s!", name)
	}

	if page == nil {
		return false, "No login browser is open — click 'Open Browser to Login' first"
	}

	c.logger.Printf("[facebook] User confirmed login, verifying...")

	if err := c.verifyLogin(page); err != nil {
		c.logger.Printf("[facebook] Verify failed: %v", err)
		return false, "Could not verify login — make sure you see your Facebook feed, then try again"
	}

	// Mark as logged in immediately so the UI updates. Profile data comes later.
	c.mu.Lock()
	if c.loggedIn {
		// Auto-detect beat us — just return success
		c.mu.Unlock()
		c.logger.Printf("[facebook] ConfirmLogin: auto-detect already set loggedIn")
		name := c.UserName()
		if name == "" || name == "Facebook User" {
			return true, "Login verified! Loading your profile..."
		}
		return true, fmt.Sprintf("Welcome, %s!", name)
	}
	c.loggedIn = true
	c.loginStatus = LoginStatus{State: "logged_in", Message: "Login verified! You can close the browser. Loading profile..."}
	c.mu.Unlock()

	// Save cookies from the headed browser
	c.logger.Printf("[facebook] Login verified! Saving session...")
	c.engine.SaveSession(platformName, page)
	c.engine.PersistSession(platformName)

	// Extract profile in a separate headless browser (background).
	// Must wait for watchForLogin to finish closing the headed browser first,
	// otherwise it will call engine.Stop() and kill our headless session.
	c.mu.RLock()
	loginDone := c.loginDone
	c.mu.RUnlock()

	go func() {
		if loginDone != nil {
			c.logger.Printf("[facebook] Waiting for login browser to close...")
			select {
			case <-loginDone:
			case <-time.After(15 * time.Second):
				c.logger.Printf("[facebook] Timed out waiting for login browser cleanup")
			}
		}
		c.extractProfileHeadless(context.Background())
	}()

	return true, "Login verified! You can close the browser window. Loading your profile..."
}

// LoginState returns the current login flow status.
func (c *Connector) LoginState() LoginStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loginStatus
}

// watchForLogin opens a headed browser to facebook.com and polls until the
// user completes login. Once detected, it saves cookies, closes the browser
// automatically, and extracts the profile in a separate headless session.
func (c *Connector) watchForLogin(ctx context.Context) {
	wasHeadless := c.engine.IsHeadless()
	c.engine.SetHeadless(false)
	c.engine.Stop()

	done := make(chan struct{})
	c.mu.Lock()
	c.loginDone = done
	c.mu.Unlock()

	cleanup := func() {
		c.engine.Stop()
		c.engine.SetHeadless(wasHeadless)
		c.mu.Lock()
		c.loginDone = nil
		c.loginPage = nil
		c.mu.Unlock()
		close(done)
	}

	if err := c.engine.Start(); err != nil {
		c.mu.Lock()
		c.loginStatus = LoginStatus{State: "error", Message: fmt.Sprintf("Failed to start browser: %v", err)}
		c.mu.Unlock()
		cleanup()
		return
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		c.mu.Lock()
		c.loginStatus = LoginStatus{State: "error", Message: fmt.Sprintf("Failed to open page: %v", err)}
		c.mu.Unlock()
		cleanup()
		return
	}

	c.logger.Printf("[facebook] Opening browser for user login...")

	if err := c.engine.NavigateAndWait(page, facebookBaseURL, loginTimeout); err != nil {
		c.mu.Lock()
		c.loginStatus = LoginStatus{State: "error", Message: fmt.Sprintf("Failed to navigate: %v", err)}
		c.mu.Unlock()
		page.Close()
		cleanup()
		return
	}

	c.dismissCookieBanner(page)

	// Browser is open and ready for the user
	c.mu.Lock()
	c.loginPage = page
	c.loginStatus = LoginStatus{State: "waiting", Message: "Browser is open — log in to Facebook. It will close automatically."}
	c.mu.Unlock()

	c.logger.Printf("[facebook] Browser ready, polling for login every 3 seconds...")

	// Poll every 3 seconds for up to 5 minutes
	timeout := time.After(5 * time.Minute)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			c.mu.Lock()
			c.loginStatus = LoginStatus{State: "error", Message: "Login timed out after 5 minutes — please try again"}
			c.mu.Unlock()
			c.logger.Printf("[facebook] watchForLogin: timed out")
			page.Close()
			cleanup()
			return

		case <-ticker.C:
			// Check if ConfirmLogin already handled this (fallback path)
			c.mu.RLock()
			alreadyDone := c.loggedIn
			c.mu.RUnlock()
			if alreadyDone {
				c.logger.Printf("[facebook] watchForLogin: login already confirmed, closing browser")
				page.Close()
				cleanup()
				return
			}

			// Auto-detect: check if user has logged in
			if err := c.verifyLogin(page); err == nil {
				c.logger.Printf("[facebook] Login detected! Saving cookies...")

				// Save cookies from the headed browser
				c.engine.SaveSession(platformName, page)
				c.engine.PersistSession(platformName)

				// Mark as logged in — profile extraction will happen headless
				c.mu.Lock()
				c.loggedIn = true
				c.loginStatus = LoginStatus{State: "logged_in", Message: "Login verified! Loading your profile..."}
				c.mu.Unlock()

				// Close the headed browser
				c.logger.Printf("[facebook] Closing login browser...")
				page.Close()
				cleanup()

				// Extract profile in a separate headless browser
				c.extractProfileHeadless(ctx)
				return
			}
		}
	}
}

// RestoreSession tries to restore a saved session from disk on boot.
// Returns true if a valid session was restored.
func (c *Connector) RestoreSession(ctx context.Context) bool {
	if err := c.engine.LoadSession(platformName); err != nil {
		return false
	}

	if !c.engine.IsLoggedIn(platformName) {
		return false
	}

	// Try to verify the session is still valid by opening a page
	if err := c.engine.Start(); err != nil {
		return false
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return false
	}
	defer page.Close()

	if err := c.engine.NavigateAndWait(page, facebookBaseURL, loginTimeout); err != nil {
		return false
	}

	browser.HumanDelay()

	if err := c.verifyLogin(page); err != nil {
		c.logger.Printf("[facebook] Saved session expired — user needs to log in again")
		return false
	}

	// Re-save session (refreshes cookies)
	c.engine.SaveSession(platformName, page)
	c.engine.PersistSession(platformName)

	// Use profile data from DB if already loaded (avoids slow extraction on every boot)
	c.mu.RLock()
	existingName := c.userName
	existingPic := c.profilePic
	c.mu.RUnlock()

	userName := existingName
	profilePic := existingPic

	// Only re-extract if we don't already have good data from the DB
	if userName == "" || userName == "Facebook User" {
		userName = c.extractUserName(page)
		profilePic = c.extractProfilePic(page)
	}

	c.mu.Lock()
	c.loggedIn = true
	c.userName = userName
	c.profilePic = profilePic
	c.loginStatus = LoginStatus{State: "logged_in", Message: fmt.Sprintf("Session restored as %s", userName)}
	c.mu.Unlock()

	c.logger.Printf("[facebook] Session restored as %s", userName)

	c.store.UpsertAccount(ctx, &store.Account{
		Channel:    platformName,
		JID:        "fb:" + userName,
		PushName:   userName,
		ProfilePic: profilePic,
		Status:     "connected",
		LastSeenAt: time.Now(),
	})

	return true
}

// extractProfileHeadless starts a new headless browser session with saved cookies,
// navigates to /me, and extracts the user's name and profile picture.
// This is called after the headed login browser is already closed.
func (c *Connector) extractProfileHeadless(ctx context.Context) {
	c.logger.Printf("[facebook] Starting headless browser to extract profile...")

	// Make sure the engine is set to headless and start it
	c.engine.SetHeadless(true)
	if err := c.engine.Start(); err != nil {
		c.logger.Printf("[facebook] Failed to start headless browser for profile: %v", err)
		return
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		c.logger.Printf("[facebook] Failed to create page for profile extraction: %v", err)
		return
	}
	defer page.Close()

	// Navigate to Facebook to make sure cookies are loaded
	if err := c.engine.NavigateAndWait(page, facebookBaseURL, loginTimeout); err != nil {
		c.logger.Printf("[facebook] Failed to navigate for profile extraction: %v", err)
		return
	}

	// Verify we're still logged in
	if err := c.verifyLogin(page); err != nil {
		c.logger.Printf("[facebook] Session not valid in headless browser: %v", err)
		return
	}

	// Extract name + pic
	userName := c.extractUserName(page)
	profilePic := c.extractProfilePic(page)

	c.mu.Lock()
	c.userName = userName
	c.profilePic = profilePic
	c.loginStatus = LoginStatus{State: "logged_in", Message: fmt.Sprintf("Logged in as %s", userName)}
	c.mu.Unlock()

	c.logger.Printf("[facebook] Profile extracted: %s (pic: %s)", userName, profilePic)

	c.store.UpsertAccount(ctx, &store.Account{
		Channel:    platformName,
		JID:        "fb:" + userName,
		PushName:   userName,
		ProfilePic: profilePic,
		Status:     "connected",
		LastSeenAt: time.Now(),
	})
}

// dismissCookieBanner tries to click the cookie accept button if present.
func (c *Connector) dismissCookieBanner(page *rod.Page) {
	selectors := []string{
		`[data-testid="cookie-policy-manage-dialog-accept-button"]`,
		`button[title="Allow all cookies"]`,
		`button[title="Allow essential and optional cookies"]`,
	}
	for _, sel := range selectors {
		el, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			_ = el.Click(proto.InputMouseButtonLeft, 1)
			browser.ShortDelay()
			return
		}
	}
}

// verifyLogin checks if we ended up on a logged-in page.
func (c *Connector) verifyLogin(page *rod.Page) error {
	// Fastest check: if the URL is NOT on /login, the user is logged in.
	// Facebook redirects to the feed after successful login.
	res, err := page.Eval(`() => window.location.href`)
	if err == nil {
		url := res.Value.Str()
		if !strings.Contains(url, "/login") && !strings.Contains(url, "/recover") &&
			!strings.Contains(url, "/checkpoint") && strings.Contains(url, "facebook.com") {
			c.logger.Printf("[facebook] Verified login via URL: %s", url)
			return nil
		}
	}

	// Check for login error indicators (quick — 1s timeout)
	errorSelectors := []string{
		`#error_box`,
		`[data-testid="login_error_message"]`,
	}
	for _, sel := range errorSelectors {
		el, err := page.Timeout(1 * time.Second).Element(sel)
		if err == nil {
			text, _ := el.Text()
			if text != "" {
				return fmt.Errorf("Facebook error: %s", strings.TrimSpace(text))
			}
		}
	}

	// Check for successful login indicators (3s each — much faster than before)
	successSelectors := []string{
		`[aria-label="Facebook"]`,
		`div[role="navigation"]`,
		`[data-pagelet="LeftRail"]`,
		`div[role="banner"]`,
	}
	for _, sel := range successSelectors {
		_, err := page.Timeout(3 * time.Second).Element(sel)
		if err == nil {
			return nil
		}
	}

	return fmt.Errorf("could not verify login — page may require 2FA or captcha")
}

// genericPageNames are titles/headings that Facebook shows in its UI chrome
// and should never be treated as a user's name.
var genericPageNames = map[string]bool{
	"facebook": true, "log in": true, "chats": true, "messenger": true,
	"home": true, "watch": true, "marketplace": true, "gaming": true,
	"groups": true, "notifications": true, "menu": true, "settings": true,
	"": true,
}

func isGenericName(name string) bool {
	return genericPageNames[strings.ToLower(strings.TrimSpace(name))]
}

// extractUserName tries to get the logged-in user's name from the page.
// It navigates the given page to /me to read the profile name.
func (c *Connector) extractUserName(page *rod.Page) string {
	// Navigate to the user's own profile page
	if err := c.engine.NavigateAndWait(page, "https://www.facebook.com/me", 20*time.Second); err != nil {
		c.logger.Printf("[facebook] Could not navigate to /me for name extraction: %v", err)
		return "Facebook User"
	}

	// Wait a bit for the SPA to settle and update the title
	time.Sleep(3 * time.Second)

	// Method 1: Look for h1 > div[role="button"] — Facebook's profile page
	// structure has the user's name inside a div with role="button" inside h1.
	for attempt := 0; attempt < 3; attempt++ {
		res, err := page.Eval(`() => {
			// Best selector: h1 containing a div[role="button"] with the name
			const btn = document.querySelector('h1 div[role="button"]');
			if (btn) {
				const name = (btn.textContent || '').trim();
				if (name) return name;
			}
			// Fallback: iterate all h1s — the profile name h1 also has a div[role="button"]
			// inside it; pick the first h1 that contains one, or the longest h1 text.
			const h1s = [...document.querySelectorAll('h1')];
			for (const h1 of h1s) {
				if (h1.querySelector('[role="button"]')) {
					const name = (h1.textContent || '').trim();
					if (name) return name;
				}
			}
			// Fallback: page title
			const title = document.title || '';
			if (title.includes('|')) return title.split('|')[0].trim();
			if (title.includes('-')) return title.split('-')[0].trim();
			return title.trim();
		}`)
		if err == nil {
			name := strings.TrimSpace(res.Value.Str())
			if !isGenericName(name) && len(name) < 100 {
				c.logger.Printf("[facebook] Found user name: %s (attempt %d)", name, attempt+1)
				return name
			}
		}
		if attempt < 2 {
			time.Sleep(2 * time.Second)
		}
	}

	// Method 3: Extract from the final URL — /me redirects to /profile.php?id=123 or /username
	res, err := page.Eval(`() => window.location.href`)
	if err == nil {
		finalURL := res.Value.Str()
		c.logger.Printf("[facebook] Profile URL: %s", finalURL)
		// If URL is like facebook.com/username (not /profile.php)
		if !strings.Contains(finalURL, "profile.php") && !strings.Contains(finalURL, "/me") {
			parts := strings.Split(strings.TrimRight(finalURL, "/"), "/")
			if len(parts) > 0 {
				slug := parts[len(parts)-1]
				if slug != "" && !isGenericName(slug) {
					// Convert slug like "john.doe" to "John Doe"
					slug = strings.ReplaceAll(slug, ".", " ")
					slug = strings.ReplaceAll(slug, "-", " ")
					slug = strings.Title(slug) //nolint:staticcheck
					c.logger.Printf("[facebook] Found user name via URL slug: %s", slug)
					return slug
				}
			}
		}
	}

	return "Facebook User"
}

// extractProfilePic finds the profile picture on the page, downloads it
// using Go's HTTP client with the browser's cookies, and returns a base64
// data URL that can be used directly in <img src>.
// Assumes the page is already on the user's profile (/me) from extractUserName.
func (c *Connector) extractProfilePic(page *rod.Page) string {
	// Give images a moment to load
	time.Sleep(2 * time.Second)

	// Step 1: Find the profile picture URL on the profile page.
	// Priority: <image> inside svg[aria-label="Profile picture actions"] (the circular avatar),
	// falling back to the largest scontent image if that isn't found.
	res, err := page.Eval(`() => {
		// Preferred: the circular profile picture SVG
		const svgs = document.querySelectorAll('svg[aria-label="Profile picture actions"]');
		for (const svg of svgs) {
			const img = svg.querySelector('image');
			if (img) {
				const src = img.getAttribute('xlink:href') || img.getAttribute('href') || '';
				if (src && src.includes('scontent')) return src;
			}
		}
		// Fallback: largest scontent image near the top of the page
		const candidates = [];
		document.querySelectorAll('image').forEach(img => {
			const src = img.getAttribute('xlink:href') || '';
			if (src && src.includes('scontent') && !src.includes('emoji') && !src.includes('static')) {
				const rect = img.getBoundingClientRect();
				candidates.push({ src, area: rect.width * rect.height, top: rect.top });
			}
		});
		document.querySelectorAll('img').forEach(img => {
			const src = img.getAttribute('src') || '';
			if (src && src.includes('scontent') && !src.includes('emoji') && !src.includes('static')) {
				const rect = img.getBoundingClientRect();
				candidates.push({ src, area: rect.width * rect.height, top: rect.top });
			}
		});
		candidates.sort((a, b) => b.area - a.area);
		for (const c of candidates) {
			if (c.top < 600 && c.area > 500) return c.src;
		}
		return candidates.length > 0 ? candidates[0].src : '';
	}`)

	var picURL string
	if err == nil {
		picURL = strings.TrimSpace(res.Value.Str())
	}

	if picURL == "" {
		c.logger.Printf("[facebook] Could not find profile pic URL on page")
		return ""
	}

	c.logger.Printf("[facebook] Found profile pic URL, downloading with cookies...")

	// Step 2: Get cookies from the browser and download via Go HTTP
	cookies, err := page.Cookies([]string{picURL})
	if err != nil {
		c.logger.Printf("[facebook] Failed to get cookies for pic download: %v", err)
		return ""
	}

	req, err := http.NewRequest("GET", picURL, nil)
	if err != nil {
		c.logger.Printf("[facebook] Failed to create pic request: %v", err)
		return ""
	}
	for _, ck := range cookies {
		req.AddCookie(&http.Cookie{Name: ck.Name, Value: ck.Value})
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.logger.Printf("[facebook] Failed to download profile pic: %v", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		c.logger.Printf("[facebook] Profile pic download returned status %d", resp.StatusCode)
		return ""
	}

	imgBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		c.logger.Printf("[facebook] Failed to read profile pic body: %v", err)
		return ""
	}

	// Determine MIME type
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = http.DetectContentType(imgBytes)
	}
	if !strings.HasPrefix(ct, "image/") {
		ct = "image/jpeg"
	}

	dataURL := "data:" + ct + ";base64," + base64.StdEncoding.EncodeToString(imgBytes)
	c.logger.Printf("[facebook] Profile pic downloaded (%d bytes, %s)", len(imgBytes), ct)
	return dataURL
}

// ---------------------------------------------------------------------------
// Posting
// ---------------------------------------------------------------------------

// CreatePost creates a text post on the user's timeline or a specific page.
func (c *Connector) CreatePost(ctx context.Context, content string, pageURL string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not logged in — please log in via the Facebook setup page first")
	}

	if err := c.engine.Start(); err != nil {
		return fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	targetURL := facebookBaseURL
	if pageURL != "" {
		targetURL = pageURL
	}

	if err := c.engine.NavigateAndWait(page, targetURL, actionTimeout); err != nil {
		return fmt.Errorf("navigate: %w", err)
	}

	browser.HumanDelay()

	// Click "What's on your mind?" area.
	// [aria-label="Create a post"] is a role="region" wrapper — click the button inside it.
	createPostSelectors := []string{
		`[aria-label="Create a post"] [role="button"]`,
		`[aria-label="Create a post"]`,
		`div[role="button"] span`,
	}

	var postArea *rod.Element
	for _, sel := range createPostSelectors {
		el, err := page.Timeout(actionTimeout).Element(sel)
		if err == nil {
			postArea = el
			break
		}
	}

	if postArea == nil {
		return fmt.Errorf("could not find post creation area — selectors may have changed")
	}

	if err := postArea.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click create post: %w", err)
	}

	browser.HumanDelay()

	// Find the text input in the post dialog
	textInputSelectors := []string{
		`div[contenteditable="true"][role="textbox"]`,
		`[aria-label="What's on your mind?"]`,
	}

	var textInput *rod.Element
	for _, sel := range textInputSelectors {
		el, err := page.Timeout(actionTimeout).Element(sel)
		if err == nil {
			textInput = el
			break
		}
	}

	if textInput == nil {
		return fmt.Errorf("could not find post text input — dialog may not have opened")
	}

	if err := textInput.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click text input: %w", err)
	}
	browser.ShortDelay()

	if err := browser.TypeLikeHuman(textInput, content); err != nil {
		return fmt.Errorf("type post content: %w", err)
	}

	browser.HumanDelay()

	// Click Post button
	postBtnSelectors := []string{
		`[aria-label="Post"]`,
		`div[role="button"] span`,
	}

	for _, sel := range postBtnSelectors {
		el, err := page.Timeout(actionTimeout).Element(sel)
		if err == nil {
			text, _ := el.Text()
			if strings.TrimSpace(text) == "Post" {
				_ = el.Click(proto.InputMouseButtonLeft, 1)
				break
			}
		}
	}

	browser.HumanDelay()
	browser.HumanDelay()

	// Cache the post
	c.store.UpsertCachedPost(ctx, &store.CachedPost{
		Platform: platformName,
		PostID:   fmt.Sprintf("local_%d", time.Now().UnixMilli()),
		Content:  content,
		PostedAt: time.Now(),
	})

	c.logger.Printf("[facebook] Post created successfully")
	return nil
}

// ---------------------------------------------------------------------------
// Messenger
// ---------------------------------------------------------------------------

// ReadMessages returns cached messages, with cache metadata.
func (c *Connector) ReadMessages(ctx context.Context, conversationID string, limit int) ([]store.CachedMessage, time.Time, bool, error) {
	messages, lastSynced, err := c.store.GetCachedMessages(ctx, platformName, conversationID, limit)
	if err == nil && len(messages) > 0 {
		return messages, lastSynced, true, nil
	}
	return nil, time.Time{}, false, nil
}

// FetchFreshMessages opens Messenger and scrapes recent messages.
func (c *Connector) FetchFreshMessages(ctx context.Context, conversationID string) ([]store.CachedMessage, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("not logged in — please log in via the Facebook setup page first")
	}

	if err := c.engine.Start(); err != nil {
		return nil, fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	targetURL := messengerURL
	if conversationID != "" {
		targetURL = messengerURL + "/t/" + conversationID
	}

	if err := c.engine.NavigateAndWait(page, targetURL, actionTimeout); err != nil {
		return nil, fmt.Errorf("navigate messenger: %w", err)
	}

	browser.HumanDelay()
	browser.HumanDelay()

	messages := c.scrapeMessages(page, conversationID)

	for i := range messages {
		c.store.UpsertCachedMessage(ctx, &messages[i])
	}

	c.logger.Printf("[facebook] Fetched %d messages", len(messages))
	return messages, nil
}

func (c *Connector) scrapeMessages(page *rod.Page, conversationID string) []store.CachedMessage {
	var messages []store.CachedMessage

	// div[role="gridcell"][data-scope="messages_table"] is the per-message element
	// in Messenger threads. div[role="row"] targets the sidebar conversation list.
	msgElements, err := page.Timeout(actionTimeout).Elements(`div[role="gridcell"][data-scope="messages_table"]`)
	if err != nil {
		c.logger.Printf("[facebook] Could not find message elements: %v", err)
		return messages
	}

	for i, el := range msgElements {
		text, err := el.Text()
		if err != nil || text == "" {
			continue
		}

		msg := store.CachedMessage{
			Platform:       platformName,
			ConversationID: conversationID,
			MessageID:      fmt.Sprintf("msg_%s_%d", conversationID, i),
			Content:        strings.TrimSpace(text),
			Timestamp:      time.Now(),
			IsOutgoing:     false,
		}
		messages = append(messages, msg)
	}

	return messages
}

// SendMessage sends a message in Facebook Messenger.
func (c *Connector) SendMessage(ctx context.Context, conversationID, message string) error {
	if !c.IsConnected() {
		return fmt.Errorf("not logged in — please log in via the Facebook setup page first")
	}

	if err := c.engine.Start(); err != nil {
		return fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	targetURL := messengerURL + "/t/" + conversationID
	if err := c.engine.NavigateAndWait(page, targetURL, actionTimeout); err != nil {
		return fmt.Errorf("navigate messenger: %w", err)
	}

	browser.HumanDelay()

	// Find message input
	inputSelectors := []string{
		`div[aria-label="Message"][contenteditable="true"]`,
		`div[role="textbox"][contenteditable="true"]`,
	}

	var inputEl *rod.Element
	for _, sel := range inputSelectors {
		el, err := page.Timeout(actionTimeout).Element(sel)
		if err == nil {
			inputEl = el
			break
		}
	}

	if inputEl == nil {
		return fmt.Errorf("could not find message input — selectors may have changed")
	}

	if err := inputEl.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click input: %w", err)
	}
	browser.ShortDelay()

	if err := browser.TypeLikeHuman(inputEl, message); err != nil {
		return fmt.Errorf("type message: %w", err)
	}

	browser.ShortDelay()

	// Press Enter to send
	if err := inputEl.Type(input.Enter); err != nil {
		return fmt.Errorf("press enter: %w", err)
	}

	browser.HumanDelay()

	c.store.UpsertCachedMessage(ctx, &store.CachedMessage{
		Platform:       platformName,
		ConversationID: conversationID,
		MessageID:      fmt.Sprintf("sent_%d", time.Now().UnixMilli()),
		Content:        message,
		Timestamp:      time.Now(),
		IsOutgoing:     true,
	})

	c.logger.Printf("[facebook] Message sent to conversation %s", conversationID)
	return nil
}

// ---------------------------------------------------------------------------
// Contacts
// ---------------------------------------------------------------------------

// GetContacts returns cached contacts.
func (c *Connector) GetContacts(ctx context.Context, query string, limit int) ([]store.CachedContact, time.Time, bool, error) {
	contacts, lastSynced, err := c.store.GetCachedContacts(ctx, platformName, query, limit)
	if err == nil && len(contacts) > 0 {
		return contacts, lastSynced, true, nil
	}
	return nil, time.Time{}, false, nil
}

// FetchFreshContacts opens Messenger and scrapes the contact list.
func (c *Connector) FetchFreshContacts(ctx context.Context) ([]store.CachedContact, error) {
	if !c.IsConnected() {
		return nil, fmt.Errorf("not logged in — please log in via the Facebook setup page first")
	}

	if err := c.engine.Start(); err != nil {
		return nil, fmt.Errorf("start browser: %w", err)
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return nil, fmt.Errorf("new page: %w", err)
	}
	defer page.Close()

	if err := c.engine.NavigateAndWait(page, messengerURL, actionTimeout); err != nil {
		return nil, fmt.Errorf("navigate messenger: %w", err)
	}

	browser.HumanDelay()
	browser.HumanDelay()

	var contacts []store.CachedContact
	// a[href^="/t/"] matches conversation links (/t/ID/) but not nav tabs like
	// /marketplace/t/ or /archived/t/. Query params (?focus_target=1) are stripped.
	convElements, err := page.Timeout(actionTimeout).Elements(`a[href^="/t/"]`)
	if err != nil {
		return nil, fmt.Errorf("find conversations: %w", err)
	}

	for _, el := range convElements {
		name, err := el.Text()
		if err != nil || name == "" {
			continue
		}

		href, err := el.Attribute("href")
		if err != nil || href == nil {
			continue
		}

		parts := strings.Split(*href, "/t/")
		if len(parts) < 2 {
			continue
		}
		convID := strings.TrimRight(parts[1], "/")
		// Strip query params (e.g. "?focus_target=1" from nav tab links)
		if idx := strings.Index(convID, "?"); idx != -1 {
			convID = convID[:idx]
		}
		if convID == "" {
			continue
		}

		contact := store.CachedContact{
			Platform:   platformName,
			ContactID:  convID,
			Name:       strings.TrimSpace(name),
			ProfileURL: messengerURL + "/t/" + convID,
		}
		contacts = append(contacts, contact)
		c.store.UpsertCachedContact(ctx, &contact)
	}

	c.logger.Printf("[facebook] Fetched %d contacts", len(contacts))
	return contacts, nil
}

// ---------------------------------------------------------------------------
// Health Check
// ---------------------------------------------------------------------------

// HealthCheck verifies the connector is working by loading Facebook and
// checking whether the session is still valid.
func (c *Connector) HealthCheck(ctx context.Context) error {
	if err := c.engine.Start(); err != nil {
		return c.saveHealthCheck(ctx, fmt.Errorf("browser failed: %w", err))
	}

	page, err := c.engine.NewPage(platformName)
	if err != nil {
		return c.saveHealthCheck(ctx, fmt.Errorf("page creation failed: %w", err))
	}
	defer page.Close()

	// Load the main Facebook page — if cookies are valid it lands on the feed,
	// if not it redirects to /login.
	if err := c.engine.NavigateAndWait(page, facebookBaseURL, healthTimeout); err != nil {
		return c.saveHealthCheck(ctx, fmt.Errorf("cannot load Facebook: %w", err))
	}

	currentURL := page.MustInfo().URL

	if c.IsConnected() {
		// We expect to be on the feed (not redirected to /login).
		// If the URL contains "/login" the session has expired.
		if strings.Contains(currentURL, "/login") {
			c.mu.Lock()
			c.loggedIn = false
			c.mu.Unlock()
			return c.saveHealthCheck(ctx, fmt.Errorf("session expired — cookies no longer valid, please log in again"))
		}
	} else {
		// Not connected — just verify Facebook loads at all.
		// Either feed or login page is fine.
	}

	// All good
	c.store.SaveHealthCheck(ctx, &store.HealthCheckResult{
		Platform:  platformName,
		Status:    "ok",
		CheckedAt: time.Now(),
	})
	c.logger.Printf("[facebook] Health check passed")
	return nil
}

func (c *Connector) saveHealthCheck(ctx context.Context, err error) error {
	c.store.SaveHealthCheck(ctx, &store.HealthCheckResult{
		Platform:  platformName,
		Status:    "failed",
		Error:     err.Error(),
		CheckedAt: time.Now(),
	})
	c.logger.Printf("[facebook] Health check FAILED: %v", err)
	return err
}

// Disconnect marks the connector as disconnected and clears saved session.
func (c *Connector) Disconnect() {
	c.mu.Lock()
	c.loggedIn = false
	c.userName = ""
	c.profilePic = ""
	c.pageInfo = nil
	c.loginStatus = LoginStatus{State: "idle"}
	c.mu.Unlock()

	// Clear persisted cookies
	c.engine.ClearSession(platformName)
}
