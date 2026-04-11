# Claude Bridge — Setup Guide

## Prerequisites

- Go 1.22+ installed
- GCC/C compiler (required for SQLite via cgo)
  - macOS: `xcode-select --install`
  - Windows: Install [TDM-GCC](https://jmeubank.github.io/tdm-gcc/) or use MSYS2
  - Linux: `sudo apt install build-essential`

## Build

```bash
cd claude-bridge
go mod tidy
CGO_ENABLED=1 go build -o claude-bridge .
```

On Windows, the output will be `claude-bridge.exe`.

## Run

```bash
# Normal mode (HTTP server + system tray)
./claude-bridge

# Headless mode (no system tray, good for servers)
./claude-bridge --no-tray

# Custom port
./claude-bridge --port 8080

# Custom data directory
./claude-bridge --data-dir /path/to/data
```

The dashboard opens at `http://127.0.0.1:10002`.

## Connect WhatsApp

1. Start Claude Bridge
2. Open `http://127.0.0.1:10002/setup/whatsapp`
3. Click **+ Add Account**
4. Scan the QR code with your phone (WhatsApp → Settings → Linked Devices → Link a Device)
5. Once connected, the account appears in the list with a green "Connected" badge

You can add multiple WhatsApp accounts. Each account has Reconnect/Disconnect controls. Session data is stored in `~/.claude-bridge/whatsapp/whatsapp.db` (SQLite) and `~/.claude-bridge/app.db` (account list). Sessions persist across restarts — you won't need to scan again unless you log out or remove the account.

## Connect to Claude Desktop

Claude Bridge has a built-in MCP server so Claude can interact with your connected accounts.

### Option A: One-click install (recommended)

1. Open the dashboard at `http://127.0.0.1:10002`
2. Click **Install to Claude** on the Claude Desktop card
3. Restart Claude Desktop

### Option B: Manual config

Open your Claude Desktop config file:

- **macOS**: `~/Library/Application Support/Claude/claude_desktop_config.json`
- **Windows**: `%APPDATA%\Claude\claude_desktop_config.json`

Add the Claude Bridge MCP server:

```json
{
  "mcpServers": {
    "claude-bridge": {
      "command": "/full/path/to/claude-bridge",
      "args": ["--mcp"]
    }
  }
}
```

Replace `/full/path/to/claude-bridge` with the actual path to your binary. If you already have other MCP servers, merge the `"claude-bridge"` entry into your existing `"mcpServers"` object.

Then restart Claude Desktop.

### Use it

Make sure the Claude Bridge dashboard is running (the MCP tools proxy to the running HTTP server), then in Claude you'll have these tools:

- **get_whatsapp_status** — Check connection status and account count
- **send_whatsapp_message** — Send a message to any phone number
- **read_whatsapp_messages** — Read recent messages (all or by chat)
- **list_whatsapp_contacts** — Search and list contacts

### How it works

Claude Bridge runs as one process with a dashboard + HTTP API. The MCP server (stdio mode) proxies Claude's tool calls to the running HTTP server:

```
Claude Desktop ←→ [stdio JSON-RPC] ←→ claude-bridge --mcp ←→ http://127.0.0.1:10002/api/* ←→ WhatsApp
```

> **Note**: Claude Desktop's "custom connectors" route through Anthropic's cloud and cannot reach localhost. That's why we use the local MCP config (stdio mode) instead — it runs the binary directly on your machine.

### Claude Code

Same approach works for Claude Code. Add to your MCP settings:

```json
{
  "mcpServers": {
    "claude-bridge": {
      "command": "/full/path/to/claude-bridge",
      "args": ["--mcp"]
    }
  }
}
```

Or test manually:

```bash
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' | ./claude-bridge --mcp
```

## Data Storage

All data is stored locally in `~/.claude-bridge/`:

- `app.db` — Account list and metadata (SQLite)
- `whatsapp/whatsapp.db` — WhatsApp sessions (SQLite, managed by whatsmeow)
- `cert.pem` / `key.pem` — Self-signed TLS certificate (auto-generated)
- Messages are stored in memory only (cleared on restart, persistent store coming in Phase 2)

## Troubleshooting

**"Cannot reach Claude Bridge" in Claude**
→ Make sure the Claude Bridge dashboard is running (`./claude-bridge`) before using Claude. The MCP tools proxy to the HTTP server on port 10002.

**QR code not appearing**
→ Check the terminal/logs for errors. Ensure you have internet access and the SQLite build succeeded.

**WhatsApp disconnects after a while**
→ This can happen if the phone goes offline for extended periods. Use the Reconnect button on the WhatsApp page, or restart Claude Bridge (it auto-reconnects saved accounts on boot).

**Build fails with "cgo: C compiler not found"**
→ Install a C compiler (see Prerequisites above). SQLite requires cgo.

**Account shows "Disconnected" after restart**
→ Claude Bridge auto-reconnects on boot. Check the logs — if the session expired, you may need to remove and re-add the account via QR scan.
