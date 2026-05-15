package agent

import (
	"context"
	"encoding/json"

	"claude-bridge/internal/store"
)

const platformAgent = "agent"

// FlowStep is one stage in the agent's conversation flow.
type FlowStep struct {
	Name        string `json:"name"`
	Instruction string `json:"instruction"`
}

// Config holds all agent settings persisted to the credentials table.
type Config struct {
	Enabled           bool       `json:"enabled"`
	SystemPrompt      string     `json:"system_prompt"`
	FlowSteps         []FlowStep `json:"flow_steps"`
	Model             string     `json:"model"`
	GlobalReplyMode   string     `json:"global_reply_mode"`   // "auto"|"review"|"off"
	OwnerJID          string     `json:"owner_jid"`           // deprecated — migrated to OwnerJIDs on load
	OwnerJIDs         []string   `json:"owner_jids"`          // admin JIDs that receive notifications + can !login
	AutoSyncFrequency string     `json:"auto_sync_frequency"` // "daily"|"weekly"|"off"
	TelegramBotToken  string     `json:"telegram_bot_token"`  // bot token from @BotFather
	OwnerTelegramIDs  []int64    `json:"owner_telegram_ids"`  // Telegram user IDs allowed to dispatch
}

// DefaultFlowSteps are prepopulated when no steps are saved yet.
var DefaultFlowSteps = []FlowStep{
	{Name: "Greet", Instruction: "Welcome the client warmly and introduce yourself by name and company."},
	{Name: "Collect Info", Instruction: "Ask for the client's full name and best contact number."},
	{Name: "Assess Need", Instruction: "Ask what type of insurance they are interested in (life, medical, motor, travel, business)."},
	{Name: "Follow Up", Instruction: "Let them know a specialist will contact them within 24 hours with a tailored recommendation."},
	{Name: "Redirect", Instruction: "If the client seems urgent or asks for immediate help, provide the sales team contact number from your knowledge base."},
}

// LoadConfig reads saved agent config. Returns defaults if unset.
func LoadConfig(ctx context.Context, s *store.Store) (Config, error) {
	cred, err := s.GetCredential(ctx, platformAgent)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if cred != nil && cred.Extra != "" {
		_ = json.Unmarshal([]byte(cred.Extra), &c)
	}
	if len(c.FlowSteps) == 0 {
		c.FlowSteps = DefaultFlowSteps
	}
	if c.GlobalReplyMode == "" {
		c.GlobalReplyMode = "auto"
	}
	if c.AutoSyncFrequency == "" {
		c.AutoSyncFrequency = "off"
	}
	// migrate legacy single owner_jid → owner_jids
	if c.OwnerJID != "" && len(c.OwnerJIDs) == 0 {
		c.OwnerJIDs = []string{c.OwnerJID}
		c.OwnerJID = ""
	}
	return c, nil
}

// SaveConfig persists agent config.
func SaveConfig(ctx context.Context, s *store.Store, c Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.SaveCredential(ctx, &store.Credential{
		Platform: platformAgent,
		Extra:    string(b),
	})
}
