package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"claude-bridge/internal/agent"
	"claude-bridge/internal/browser"
	"claude-bridge/internal/claude"
	"claude-bridge/internal/connectors/facebook"
	"claude-bridge/internal/connectors/telegram"
	"claude-bridge/internal/connectors/whatsapp"
	"claude-bridge/internal/knowledge"
	"claude-bridge/internal/mcp"
	"claude-bridge/internal/server"
	"claude-bridge/internal/store"
	"claude-bridge/internal/tray"
	"claude-bridge/internal/updater"
)

// bootTelegram constructs and starts a Telegram client from saved agent
// config. Returns nil if no token is configured or no owner IDs allowlisted.
// In P1 the handler is a bare echo — P2 will swap in the dispatch loop.
func bootTelegram(ctx context.Context, appStore *store.Store) *telegram.Client {
	cfg, err := agent.LoadConfig(ctx, appStore)
	if err != nil {
		log.Printf("telegram: LoadConfig failed: %v", err)
		return nil
	}
	if cfg.TelegramBotToken == "" {
		return nil
	}
	if len(cfg.OwnerTelegramIDs) == 0 {
		log.Printf("telegram: bot token set but no owner_telegram_ids — bot will drop all messages")
	}
	c := telegram.New(cfg.TelegramBotToken, cfg.OwnerTelegramIDs)
	c.SetHandler(func(ctx context.Context, m *telegram.Message) string {
		// P1 echo handler — swapped to dispatch in P2.
		return "Got: " + m.Text
	})
	go func() {
		if err := c.Start(ctx); err != nil && ctx.Err() == nil {
			log.Printf("telegram: stopped: %v", err)
		}
	}()
	return c
}

// version is embedded at build time via -ldflags "-X main.version=<git-sha>".
var version = "dev"

const (
	defaultPort    = 10002
	defaultTLSPort = 10003
)

func main() {
	mcpMode := flag.Bool("mcp", false, "Run as MCP server for Claude Desktop (stdio JSON-RPC)")
	port := flag.Int("port", defaultPort, "HTTP server port")
	tlsPort := flag.Int("tls-port", defaultTLSPort, "HTTPS server port (for Claude Desktop connector)")
	noTray := flag.Bool("no-tray", false, "Run without system tray (headless mode)")
	dataDir := flag.String("data-dir", "", "Directory for session data (default: ~/.claude-bridge)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	// Resolve data directory.
	dd := *dataDir
	if dd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Cannot determine home directory: %v", err)
		}
		dd = filepath.Join(home, ".claude-bridge")
	}

	// MCP stdio mode: run the stdio MCP server and exit.
	// This is a fallback for Claude Code or manual testing.
	if *mcpMode {
		baseURL := fmt.Sprintf("http://127.0.0.1:%d", *port)
		mcpServer := mcp.NewStdioServer(baseURL)
		if err := mcpServer.Run(); err != nil {
			log.Fatalf("MCP server error: %v", err)
		}
		return
	}

	// Open app-level SQLite store.
	appStore, err := store.New(dd)
	if err != nil {
		log.Fatalf("Failed to open app store: %v", err)
	}
	defer appStore.Close()

	// Normal mode: start HTTP + HTTPS servers + optional system tray.
	wa := whatsapp.NewManager(filepath.Join(dd, "whatsapp"), appStore)

	// Browser engine for all browser-based connectors (Facebook, Instagram, etc).
	browserEngine := browser.NewEngine(filepath.Join(dd, "browser"), true) // headless by default
	browserEngine.EnsureBrowser() // start checking/downloading Chrome in background
	fb := facebook.New(browserEngine, appStore)

	// Boot reconnects previously connected WhatsApp accounts.
	if err := wa.Boot(); err != nil {
		log.Printf("WARNING: WhatsApp boot failed: %v", err)
	}

	// Boot loads saved Facebook Messenger OAuth config and starts polling if connected.
	fb.Boot(context.Background())

	// Boot the knowledge subsystem: load config + API key, start pipeline + watcher.
	knowCtx := context.Background()
	knowCfg, _ := knowledge.LoadConfig(knowCtx, appStore)
	knowClient := claude.New("", knowCfg.Model)
	knowPipeline := knowledge.NewPipeline(appStore, knowClient)
	knowPipeline.Configure(knowCfg)

	// Attach Ollama embedder for vector search (optional — silently skipped if Ollama not running).
	knowEmbedder := knowledge.NewEmbedder(knowCfg.OllamaURL, knowCfg.EmbedModel)
	if knowEmbedder.Available(knowCtx) {
		knowPipeline.SetEmbedder(knowEmbedder)
		log.Printf("[knowledge] Ollama available at %s — vector search enabled", knowEmbedder.BaseURL())
	} else {
		log.Printf("[knowledge] Ollama not available — using FTS5 keyword search only")
		knowEmbedder = nil
	}

	knowPipeline.Start()
	knowWatcher := knowledge.NewWatcher(appStore, knowPipeline)
	if knowCfg.FolderPath != "" {
		if err := knowWatcher.SetFolder(knowCfg.FolderPath); err != nil {
			log.Printf("WARNING: knowledge watcher failed to start: %v", err)
		}
	}

	// Boot the auto-reply agent subsystem.
	agentReplier := agent.NewReplier(knowClient, appStore)
	if knowEmbedder != nil {
		agentReplier.SetEmbedder(knowEmbedder)
	}
	agentRunner := agent.NewRunner(agentReplier, wa.SendMessage, appStore)
	agentRunner.Start()
	wa.SetAgentCallback(agentRunner.Enqueue)

	srv := server.New(wa, fb, appStore, browserEngine, *port)
	srv.SetKnowledge(knowClient, knowPipeline, knowWatcher)
	srv.SetEmbedder(knowEmbedder)
	srv.SetAgent(agentRunner)

	// Start Telegram connector if configured. Owner-allowlisted long-poll;
	// P1 ships an echo handler so we can verify the wiring before P2 swaps in
	// the dispatch loop.
	tgClient := bootTelegram(context.Background(), appStore)
	if tgClient != nil {
		srv.SetTelegram(tgClient)
	}

	if err := srv.Start(); err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}

	// Start self-updater: checks GitHub Releases every 6h, signals dashboard when ready.
	upd := updater.New(version, func() { srv.SetUpdateReady() })
	upd.Start()

	// Start HTTPS server for Claude Desktop (requires https:// URL).
	if err := srv.StartTLS(*tlsPort, dd); err != nil {
		log.Printf("WARNING: HTTPS server failed to start: %v", err)
		log.Printf("  Claude Desktop connector will not work without HTTPS.")
		log.Printf("  The HTTP dashboard at http://127.0.0.1:%d still works.", *port)
	}

	log.Printf("─────────────────────────────────────────────────")
	log.Printf("  Dashboard:  http://127.0.0.1:%d", *port)
	log.Printf("  Claude URL: https://127.0.0.1:%d/mcp/sse", *tlsPort)
	log.Printf("  Data dir:   %s", dd)
	log.Printf("─────────────────────────────────────────────────")

	if *noTray {
		// Headless mode: wait for interrupt signal.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		wa.DisconnectAll()
		browserEngine.Stop()
		knowWatcher.Stop()
		knowPipeline.Stop()
		srv.Stop()
	} else {
		// System tray mode: blocks until user quits via tray.
		tray.Run(*port, func() {
			log.Println("Shutting down via tray...")
			wa.DisconnectAll()
			browserEngine.Stop()
			knowWatcher.Stop()
			knowPipeline.Stop()
			srv.Stop()
		})
	}
}
