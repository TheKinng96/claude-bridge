// Package browser provides a shared browser automation engine using go-rod.
// All browser-based connectors (Facebook, Instagram, LinkedIn, Xiao Hong Shu)
// use this engine for login, session management, and page interaction.
package browser

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

// Setup status constants.
const (
	StatusNotChecked  = "not_checked"
	StatusChecking    = "checking"
	StatusDownloading = "downloading"
	StatusReady       = "ready"
	StatusError       = "error"
)

// SetupStatus holds the current state of browser/Chromium setup.
type SetupStatus struct {
	Status  string `json:"status"`            // not_checked, checking, downloading, ready, error
	Message string `json:"message,omitempty"`  // human-readable status message
	Error   string `json:"error,omitempty"`    // error message if status=error
	BinPath string `json:"bin_path,omitempty"` // path to Chrome/Chromium binary once found
}

// Engine manages a shared browser instance and per-platform sessions.
type Engine struct {
	mu       sync.Mutex
	dataDir  string
	browser  *rod.Browser
	headless bool
	sessions map[string]*Session // keyed by platform name
	logger   *log.Logger

	// Setup tracking
	setupMu     sync.RWMutex
	setupStatus SetupStatus
}

// Session represents a browser session for a specific platform.
type Session struct {
	Platform string
	Page     *rod.Page
	Cookies  []*proto.NetworkCookie
	LoggedIn bool
	LastUsed time.Time
}

// NewEngine creates a browser engine. dataDir is where browser profiles and
// session data are stored. headless controls whether the browser is visible.
func NewEngine(dataDir string, headless bool) *Engine {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		log.Printf("[browser] Warning: cannot create data dir: %v", err)
	}
	return &Engine{
		dataDir:  dataDir,
		headless: headless,
		sessions: make(map[string]*Session),
		logger:   log.New(os.Stderr, "[browser] ", log.LstdFlags),
		setupStatus: SetupStatus{
			Status:  StatusNotChecked,
			Message: "Browser engine not yet initialized",
		},
	}
}

// ---------------------------------------------------------------------------
// Setup / Chromium management
// ---------------------------------------------------------------------------

// GetSetupStatus returns the current setup status (thread-safe).
func (e *Engine) GetSetupStatus() SetupStatus {
	e.setupMu.RLock()
	defer e.setupMu.RUnlock()
	return e.setupStatus
}

func (e *Engine) setSetupStatus(status, message, errMsg, binPath string) {
	e.setupMu.Lock()
	defer e.setupMu.Unlock()
	e.setupStatus = SetupStatus{
		Status:  status,
		Message: message,
		Error:   errMsg,
		BinPath: binPath,
	}
	e.logger.Printf("Setup status: %s — %s", status, message)
}

// IsReady returns true if the browser engine is set up and ready to use.
func (e *Engine) IsReady() bool {
	e.setupMu.RLock()
	defer e.setupMu.RUnlock()
	return e.setupStatus.Status == StatusReady
}

// EnsureBrowser checks if Chrome/Chromium is available, downloads if needed.
// This should be called on app startup. It runs in the background and updates
// the setup status so the dashboard can poll it.
func (e *Engine) EnsureBrowser() {
	go e.ensureBrowserSync()
}

func (e *Engine) ensureBrowserSync() {
	e.setSetupStatus(StatusChecking, "Looking for Chrome/Chromium...", "", "")

	// launcher.New() will resolve the browser binary path.
	// If Chrome is found on the system, it uses that.
	// If not, it downloads Chromium automatically.
	// We detect which case by checking the path before calling Launch().
	l := launcher.New()

	// ResolveURL finds or downloads the browser.
	// This is where the download happens if Chrome isn't installed.
	e.setSetupStatus(StatusDownloading, "Setting up browser engine (downloads ~150MB if Chrome not found)...", "", "")

	controlURL, err := l.Launch()
	if err != nil {
		e.setSetupStatus(StatusError, "Failed to set up browser", err.Error(), "")
		return
	}

	// Browser launched successfully — close it immediately, we just needed to ensure it exists.
	b := rod.New().ControlURL(controlURL)
	if err := b.Connect(); err == nil {
		b.Close()
	}

	e.setSetupStatus(StatusReady, "Browser engine ready", "", controlURL)
}

// CheckAndSetup triggers the browser check if not already done, returns current status.
func (e *Engine) CheckAndSetup() SetupStatus {
	current := e.GetSetupStatus()
	if current.Status == StatusReady || current.Status == StatusDownloading || current.Status == StatusChecking {
		return current
	}

	// Not yet checked — trigger async check
	e.EnsureBrowser()
	return e.GetSetupStatus()
}

// ---------------------------------------------------------------------------
// Browser lifecycle
// ---------------------------------------------------------------------------

// Start launches the browser process if not already running.
func (e *Engine) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.browser != nil {
		return nil // already running
	}

	// Check setup status
	status := e.GetSetupStatus()
	if status.Status == StatusDownloading {
		return fmt.Errorf("browser engine is still downloading Chromium — please wait")
	}
	if status.Status == StatusError {
		return fmt.Errorf("browser setup failed: %s", status.Error)
	}

	profileDir := filepath.Join(e.dataDir, "chrome-profile")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}

	// Prefer the user's real Chrome/Edge to avoid bot fingerprinting.
	// launcher.LookPath() finds Chrome, Edge, or Chromium on the system.
	chromePath, hasSystem := launcher.LookPath()
	l := launcher.New()
	if hasSystem {
		l = l.Bin(chromePath)
		e.logger.Printf("Using system browser: %s", chromePath)
	} else {
		e.logger.Printf("No system Chrome found — using bundled Chromium")
	}

	l = l.
		UserDataDir(profileDir).
		Headless(e.headless).
		Set("disable-gpu").
		Set("no-sandbox").
		Set("disable-dev-shm-usage").
		Set("disable-blink-features", "AutomationControlled").
		Set("disable-infobars").
		Set("excludeSwitches", "enable-automation").
		Set("useAutomationExtension", "false")

	controlURL, err := l.Launch()
	if err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return fmt.Errorf("connect browser: %w", err)
	}

	// Apply stealth: remove webdriver detection flags.
	if err := e.applyStealth(browser); err != nil {
		e.logger.Printf("Warning: stealth patches failed: %v", err)
	}

	e.browser = browser
	e.logger.Printf("Browser started (headless=%v, profile=%s)", e.headless, profileDir)

	// Mark as ready if not already
	if !e.IsReady() {
		e.setSetupStatus(StatusReady, "Browser running", "", "")
	}

	return nil
}

// applyStealth removes common bot detection markers.
func (e *Engine) applyStealth(b *rod.Browser) error {
	// We'll apply per-page stealth in NewPage instead since go-rod
	// doesn't support browser-level JS injection the same way.
	return nil
}

// applyPageStealth injects stealth JS into a page to avoid bot detection.
func (e *Engine) applyPageStealth(page *rod.Page) error {
	stealth := `() => {
		// Remove webdriver flag
		Object.defineProperty(navigator, 'webdriver', {get: () => false});
		try { delete navigator.__proto__.webdriver; } catch(e) {}

		// Mock plugins — return realistic plugin array
		Object.defineProperty(navigator, 'plugins', {
			get: () => {
				var plugins = [
					{name: 'Chrome PDF Plugin', filename: 'internal-pdf-viewer', description: 'Portable Document Format', length: 1},
					{name: 'Chrome PDF Viewer', filename: 'mhjfbmdgcfjbbpaeojofohoefgiehjai', description: '', length: 1},
					{name: 'Native Client', filename: 'internal-nacl-plugin', description: '', length: 1}
				];
				plugins.refresh = function() {};
				return plugins;
			}
		});

		// Mock languages
		Object.defineProperty(navigator, 'languages', {
			get: () => ['en-US', 'en']
		});

		// Fix chrome runtime — must look like real Chrome
		if (!window.chrome) window.chrome = {};
		window.chrome.runtime = window.chrome.runtime || {
			connect: function() {},
			sendMessage: function() {},
			onMessage: {addListener: function() {}, removeListener: function() {}}
		};
		window.chrome.loadTimes = window.chrome.loadTimes || function() { return {}; };
		window.chrome.csi = window.chrome.csi || function() { return {}; };

		// Mock permissions
		if (window.navigator && window.navigator.permissions && window.navigator.permissions.query) {
			var originalQuery = window.navigator.permissions.query.bind(window.navigator.permissions);
			window.navigator.permissions.query = function(parameters) {
				return parameters.name === 'notifications' ?
					Promise.resolve({ state: Notification.permission }) :
					originalQuery(parameters);
			};
		}

		// Hide WebGL fingerprinting
		try {
			var getParam = WebGLRenderingContext.prototype.getParameter;
			WebGLRenderingContext.prototype.getParameter = function(parameter) {
				if (parameter === 37445) return 'Intel Inc.';
				if (parameter === 37446) return 'Intel Iris OpenGL Engine';
				return getParam.call(this, parameter);
			};
		} catch(e) {}
	}`

	_, err := page.Eval(stealth)
	return err
}

// NewPage creates a new browser page (tab) for a given platform.
// If a session exists for the platform, it reuses cookies.
func (e *Engine) NewPage(platform string) (*rod.Page, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.browser == nil {
		return nil, fmt.Errorf("browser not started — call Start() first")
	}

	page, err := e.browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return nil, fmt.Errorf("create page: %w", err)
	}

	// Set a realistic viewport
	if err := page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:  1280,
		Height: 800,
	}); err != nil {
		e.logger.Printf("Warning: set viewport failed: %v", err)
	}

	// Inject stealth scripts
	if err := e.applyPageStealth(page); err != nil {
		e.logger.Printf("Warning: page stealth failed: %v", err)
	}

	// Restore cookies if we have a saved session
	if sess, ok := e.sessions[platform]; ok && len(sess.Cookies) > 0 {
		var params []*proto.NetworkCookieParam
		for _, c := range sess.Cookies {
			params = append(params, &proto.NetworkCookieParam{
				Name:     c.Name,
				Value:    c.Value,
				Domain:   c.Domain,
				Path:     c.Path,
				Secure:   c.Secure,
				HTTPOnly: c.HTTPOnly,
			})
		}
		if err := page.SetCookies(params); err != nil {
			e.logger.Printf("Warning: restore cookies for %s failed: %v", platform, err)
		} else {
			e.logger.Printf("Restored %d cookies for %s", len(params), platform)
		}
	}

	return page, nil
}

// SaveSession captures cookies from the current page and stores them for the platform.
func (e *Engine) SaveSession(platform string, page *rod.Page) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	cookies, err := page.Cookies(nil)
	if err != nil {
		return fmt.Errorf("get cookies: %w", err)
	}

	e.sessions[platform] = &Session{
		Platform: platform,
		Cookies:  cookies,
		LoggedIn: true,
		LastUsed: time.Now(),
	}

	e.logger.Printf("Saved %d cookies for %s", len(cookies), platform)
	return nil
}

// GetSession returns the session for a platform, or nil if none exists.
func (e *Engine) GetSession(platform string) *Session {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sessions[platform]
}

// IsLoggedIn checks if a platform has an active session.
func (e *Engine) IsLoggedIn(platform string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	sess, ok := e.sessions[platform]
	return ok && sess.LoggedIn
}

// SetHeadless changes the headless mode. Requires browser restart to take effect.
func (e *Engine) SetHeadless(headless bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.headless = headless
}

// IsHeadless returns the current headless setting.
func (e *Engine) IsHeadless() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.headless
}

// WaitForElement waits for a CSS selector to appear, with a timeout.
// Returns an error if the element doesn't appear (used for health checks).
func (e *Engine) WaitForElement(page *rod.Page, selector string, timeout time.Duration) error {
	_, err := page.Timeout(timeout).Element(selector)
	if err != nil {
		return fmt.Errorf("element %q not found within %v: %w", selector, timeout, err)
	}
	return nil
}

// NavigateAndWait navigates to a URL and waits for the page to load.
func (e *Engine) NavigateAndWait(page *rod.Page, url string, timeout time.Duration) error {
	p := page.Timeout(timeout)

	if err := p.Navigate(url); err != nil {
		return fmt.Errorf("navigate to %s: %w", url, err)
	}

	if err := p.WaitLoad(); err != nil {
		return fmt.Errorf("wait load %s: %w", url, err)
	}

	return nil
}

// Screenshot takes a screenshot of the current page (for health checks/debugging).
func (e *Engine) Screenshot(page *rod.Page, savePath string) error {
	data, err := page.Screenshot(true, nil)
	if err != nil {
		return fmt.Errorf("screenshot: %w", err)
	}
	return os.WriteFile(savePath, data, 0644)
}

// HumanDelay adds a random human-like delay to avoid bot detection.
func HumanDelay() {
	// Random delay between 500ms and 2s
	base := 500 * time.Millisecond
	jitter := time.Duration(time.Now().UnixNano()%1500) * time.Millisecond
	time.Sleep(base + jitter)
}

// ShortDelay adds a shorter delay for quick actions.
func ShortDelay() {
	base := 200 * time.Millisecond
	jitter := time.Duration(time.Now().UnixNano()%500) * time.Millisecond
	time.Sleep(base + jitter)
}

// TypeLikeHuman types text into an element with random delays between characters.
// Uses el.Input to set the value (works for both input fields and contenteditable).
func TypeLikeHuman(el *rod.Element, text string) error {
	// Use Input to set the full text — it handles both <input> and contenteditable.
	if err := el.Input(text); err != nil {
		return err
	}
	// Add a human-like delay after typing
	delay := time.Duration(len(text)*30+200) * time.Millisecond
	time.Sleep(delay)
	return nil
}

// PersistSession saves a platform's cookies to disk so they survive app restarts.
func (e *Engine) PersistSession(platform string) error {
	e.mu.Lock()
	sess, ok := e.sessions[platform]
	e.mu.Unlock()

	if !ok || len(sess.Cookies) == 0 {
		return fmt.Errorf("no session to persist for %s", platform)
	}

	cookieFile := filepath.Join(e.dataDir, platform+"_cookies.json")
	data, err := json.Marshal(sess.Cookies)
	if err != nil {
		return fmt.Errorf("marshal cookies: %w", err)
	}

	if err := os.WriteFile(cookieFile, data, 0600); err != nil {
		return fmt.Errorf("write cookies file: %w", err)
	}

	e.logger.Printf("Persisted %d cookies for %s to disk", len(sess.Cookies), platform)
	return nil
}

// LoadSession loads a platform's cookies from disk into the in-memory session.
func (e *Engine) LoadSession(platform string) error {
	cookieFile := filepath.Join(e.dataDir, platform+"_cookies.json")
	data, err := os.ReadFile(cookieFile)
	if err != nil {
		return fmt.Errorf("read cookies file: %w", err)
	}

	var cookies []*proto.NetworkCookie
	if err := json.Unmarshal(data, &cookies); err != nil {
		return fmt.Errorf("unmarshal cookies: %w", err)
	}

	if len(cookies) == 0 {
		return fmt.Errorf("no cookies in file for %s", platform)
	}

	e.mu.Lock()
	e.sessions[platform] = &Session{
		Platform: platform,
		Cookies:  cookies,
		LoggedIn: true,
		LastUsed: time.Now(),
	}
	e.mu.Unlock()

	e.logger.Printf("Loaded %d cookies for %s from disk", len(cookies), platform)
	return nil
}

// ClearSession removes a platform's saved cookies from both memory and disk.
func (e *Engine) ClearSession(platform string) {
	e.mu.Lock()
	delete(e.sessions, platform)
	e.mu.Unlock()

	cookieFile := filepath.Join(e.dataDir, platform+"_cookies.json")
	os.Remove(cookieFile)
	e.logger.Printf("Cleared session for %s", platform)
}

// Stop closes the browser and cleans up.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.browser != nil {
		if err := e.browser.Close(); err != nil {
			e.logger.Printf("Warning: browser close error: %v", err)
		}
		e.browser = nil
		e.logger.Println("Browser stopped")
	}
}
