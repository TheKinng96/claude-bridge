package knowledge

import (
	"context"
	"encoding/json"
	"time"

	"claude-bridge/internal/store"
)

const platformKnowledge = "knowledge"

// Config is what the dashboard reads/writes.
type Config struct {
	FolderPath  string    `json:"folder_path"`
	Model       string    `json:"model"`
	LastScanAt  time.Time `json:"last_scan_at,omitempty"`
}

// LoadConfig reads the saved folder + model. Returns zero Config if unset.
func LoadConfig(ctx context.Context, s *store.Store) (Config, error) {
	cred, err := s.GetCredential(ctx, platformKnowledge)
	if err != nil || cred == nil {
		return Config{}, err
	}
	var c Config
	if cred.Extra != "" {
		_ = json.Unmarshal([]byte(cred.Extra), &c)
	}
	return c, nil
}

// SaveConfig persists folder + model.
func SaveConfig(ctx context.Context, s *store.Store, c Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.SaveCredential(ctx, &store.Credential{
		Platform: platformKnowledge,
		Extra:    string(b),
	})
}

// UpdateLastScan stamps the config's last_scan_at.
func UpdateLastScan(ctx context.Context, s *store.Store) error {
	c, err := LoadConfig(ctx, s)
	if err != nil {
		return err
	}
	c.LastScanAt = time.Now()
	return SaveConfig(ctx, s, c)
}
