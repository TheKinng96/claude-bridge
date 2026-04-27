package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"time"

	"github.com/minio/selfupdate"
)

const (
	repo          = "TheKinng96/claude-bridge"
	checkInterval = 6 * time.Hour
	startupDelay  = 30 * time.Second
)

// Updater checks GitHub Releases for a newer binary, downloads it via selfupdate,
// and calls onReady() so the caller can notify the dashboard.
type Updater struct {
	currentVersion string
	onReady        func()
	client         *http.Client
}

func New(currentVersion string, onReady func()) *Updater {
	return &Updater{
		currentVersion: currentVersion,
		onReady:        onReady,
		client:         &http.Client{Timeout: 60 * time.Second},
	}
}

// Start launches a background goroutine that checks every 6 hours.
func (u *Updater) Start() {
	go func() {
		time.Sleep(startupDelay) // let the app finish startup before first check
		for {
			if err := u.Check(context.Background()); err != nil {
				log.Printf("[updater] check failed: %v", err)
			}
			time.Sleep(checkInterval)
		}
	}()
}

// Check fetches the latest GitHub Release, compares to currentVersion,
// and if different downloads + applies the new binary then calls onReady.
func (u *Updater) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "claude-bridge-updater/"+u.currentVersion)

	resp, err := u.client.Do(req)
	if err != nil {
		return fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("decode release: %w", err)
	}

	if release.TagName == "" {
		return fmt.Errorf("empty tag_name in release response")
	}
	if release.TagName == u.currentVersion || u.currentVersion == "dev" {
		log.Printf("[updater] up to date (%s)", u.currentVersion)
		return nil
	}

	log.Printf("[updater] new version %s available (current: %s) — downloading", release.TagName, u.currentVersion)

	bin := "claude-bridge-amd64"
	if runtime.GOARCH == "arm64" {
		bin = "claude-bridge-arm64"
	}
	dlURL := fmt.Sprintf("https://github.com/%s/releases/latest/download/%s", repo, bin)

	dlReq, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
	if err != nil {
		return err
	}
	dlReq.Header.Set("User-Agent", "claude-bridge-updater/"+u.currentVersion)

	dlResp, err := u.client.Do(dlReq)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: status %d for %s", dlResp.StatusCode, dlURL)
	}

	if err := selfupdate.Apply(dlResp.Body, selfupdate.Options{}); err != nil {
		return fmt.Errorf("selfupdate apply: %w", err)
	}

	log.Printf("[updater] binary replaced with %s — restart to apply", release.TagName)
	u.onReady()
	return nil
}
