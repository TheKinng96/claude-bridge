package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"crm-agent/internal/connectors/facebook"
	"crm-agent/internal/connectors/whatsapp"
	"crm-agent/internal/mcp"
	"crm-agent/internal/server"
	"crm-agent/internal/store"
	"crm-agent/internal/tray"
)

const (
	defaultPort    = 10002
	defaultTLSPort = 10003
)

func main() {
	mcpMode := flag.Bool("mcp", false, "Run as MCP server for Claude Desktop (stdio JSON-RPC)")
	port := flag.Int("port", defaultPort, "HTTP server port")
	tlsPort := flag.Int("tls-port", defaultTLSPort, "HTTPS server port (for Claude Desktop connector)")
	noTray := flag.Bool("no-tray", false, "Run without system tray (headless mode)")
	dataDir := flag.String("data-dir", "", "Directory for session data (default: ~/.crm-agent)")
	flag.Parse()

	// Resolve data directory.
	dd := *dataDir
	if dd == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("Cannot determine home directory: %v", err)
		}
		dd = filepath.Join(home, ".crm-agent")
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
	fb := facebook.New()

	// Boot reconnects previously connected WhatsApp accounts.
	if err := wa.Boot(); err != nil {
		log.Printf("WARNING: WhatsApp boot failed: %v", err)
	}

	srv := server.New(wa, fb, *port)
	if err := srv.Start(); err != nil {
		log.Fatalf("Failed to start HTTP server: %v", err)
	}

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
		srv.Stop()
	} else {
		// System tray mode: blocks until user quits via tray.
		tray.Run(*port, func() {
			log.Println("Shutting down via tray...")
			wa.DisconnectAll()
			srv.Stop()
		})
	}
}
