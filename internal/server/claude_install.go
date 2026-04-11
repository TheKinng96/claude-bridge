package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
)

// handleClaudeInstallCheck returns the current state of the Claude Desktop config.
// GET /api/claude/status
func (s *Server) handleClaudeInstallCheck(w http.ResponseWriter, r *http.Request) {
	configPath := claudeConfigPath()
	if configPath == "" {
		writeJSON(w, map[string]interface{}{
			"supported": false,
			"reason":    "Unsupported OS: " + runtime.GOOS,
		})
		return
	}

	installed := false
	configExists := false

	data, err := os.ReadFile(configPath)
	if err == nil {
		configExists = true
		var cfg map[string]interface{}
		if json.Unmarshal(data, &cfg) == nil {
			if servers, ok := cfg["mcpServers"].(map[string]interface{}); ok {
				if _, ok := servers["claude-bridge"]; ok {
					installed = true
				}
			}
		}
	}

	binaryPath, _ := os.Executable()
	binaryPath, _ = filepath.EvalSymlinks(binaryPath)

	writeJSON(w, map[string]interface{}{
		"supported":     true,
		"config_path":   configPath,
		"config_exists": configExists,
		"installed":     installed,
		"binary_path":   binaryPath,
	})
}

// handleClaudeInstall writes the Claude Bridge MCP server entry into the Claude Desktop config.
// POST /api/claude/install
func (s *Server) handleClaudeInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	configPath := claudeConfigPath()
	if configPath == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "Unsupported OS"})
		return
	}

	binaryPath, err := os.Executable()
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "Cannot determine binary path: " + err.Error()})
		return
	}
	binaryPath, _ = filepath.EvalSymlinks(binaryPath)

	// Read existing config or start fresh.
	var cfg map[string]interface{}

	data, err := os.ReadFile(configPath)
	if err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			// Config exists but is invalid JSON — back it up and start fresh.
			backupPath := configPath + ".backup"
			os.WriteFile(backupPath, data, 0644)
			log.Printf("[claude-install] Backed up invalid config to %s", backupPath)
			cfg = make(map[string]interface{})
		}
	} else {
		cfg = make(map[string]interface{})
	}

	// Ensure mcpServers map exists.
	servers, ok := cfg["mcpServers"].(map[string]interface{})
	if !ok {
		servers = make(map[string]interface{})
	}

	// Add or update claude-bridge entry.
	servers["claude-bridge"] = map[string]interface{}{
		"command": binaryPath,
		"args":    []string{"--mcp"},
	}
	cfg["mcpServers"] = servers

	// Write back.
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "JSON marshal: " + err.Error()})
		return
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "Create config dir: " + err.Error()})
		return
	}

	if err := os.WriteFile(configPath, append(out, '\n'), 0644); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "Write config: " + err.Error()})
		return
	}

	log.Printf("[claude-install] Installed Claude Bridge MCP server in %s", configPath)
	writeJSON(w, map[string]interface{}{
		"ok":          true,
		"config_path": configPath,
		"binary_path": binaryPath,
	})
}

// handleClaudeUninstall removes the Claude Bridge entry from the Claude Desktop config.
// POST /api/claude/uninstall
func (s *Server) handleClaudeUninstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	configPath := claudeConfigPath()
	if configPath == "" {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "Unsupported OS"})
		return
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		writeJSON(w, map[string]interface{}{"ok": true, "message": "Config file not found — nothing to remove"})
		return
	}

	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		writeJSON(w, map[string]interface{}{"ok": false, "error": "Invalid config JSON"})
		return
	}

	servers, ok := cfg["mcpServers"].(map[string]interface{})
	if !ok {
		writeJSON(w, map[string]interface{}{"ok": true, "message": "No MCP servers in config"})
		return
	}

	delete(servers, "claude-bridge")
	cfg["mcpServers"] = servers

	out, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(configPath, append(out, '\n'), 0644)

	log.Printf("[claude-install] Removed Claude Bridge from %s", configPath)
	writeJSON(w, map[string]interface{}{"ok": true})
}

// claudeConfigPath returns the platform-specific path to claude_desktop_config.json.
func claudeConfigPath() string {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "Claude", "claude_desktop_config.json")
	case "linux":
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json")
	default:
		return ""
	}
}

