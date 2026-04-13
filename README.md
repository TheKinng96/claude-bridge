# Claude Bridge

Bridge your personal accounts to Claude. Connect WhatsApp, Xiao Hong Shu, LinkedIn, Instagram, Facebook, and more — then let Claude work with them through MCP (Model Context Protocol).

## What it does

Claude Bridge is a single Go binary that runs locally on your machine. It connects your accounts and gives Claude Desktop tools to interact with them.

**Currently supported:**

- **WhatsApp** — Link your personal WhatsApp via QR code scan. Claude can read messages, send replies, and search contacts.
- **Xiao Hong Shu** *(coming soon)* — Connect your Xiao Hong Shu account. Claude can create and manage posts.
- **LinkedIn** *(coming soon)* — Connect your LinkedIn profile. Claude can create posts and manage content.
- **Instagram** *(coming soon)* — Connect your Instagram account. Claude can create posts and manage content.
- **Facebook Pages** *(coming soon)* — Connect your Facebook Page. Claude can create posts, schedule content, and check engagement.

**Features:**

- **One-click Claude install** — Dashboard button that automatically adds Claude Bridge to your Claude Desktop config
- **Multi-account** — Connect multiple WhatsApp numbers, each with reconnect/disconnect controls
- **Web dashboard** — Manage all your connections from a browser at `http://127.0.0.1:10002`
- **Daily health check** — Dashboard auto-verifies everything is working on first visit each day
- **System tray** — Runs in the background with a menu bar icon (macOS) or notification area (Windows)
- **Single binary** — No runtime dependencies. UI, API, and MCP server all embedded in one binary
- **Privacy first** — Everything runs locally. No data leaves your machine except to the services you connect

## Quick start

```bash
# Build (requires Go 1.22+ and a C compiler for SQLite)
CGO_ENABLED=1 go build -o claude-bridge .

# Run
./claude-bridge
```

Open `http://127.0.0.1:10002` to access the dashboard.

### Connect WhatsApp

1. Go to the **WhatsApp** page in the dashboard
2. Click **+ Add Account**
3. Scan the QR code with your phone (WhatsApp → Linked Devices → Link a Device)

### Connect to Claude Desktop

Click **Install to Claude** on the dashboard — it automatically writes the MCP config. Then restart Claude Desktop.

Or manually add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "claude-bridge": {
      "command": "/path/to/claude-bridge",
      "args": ["--mcp"]
    }
  }
}
```

## MCP Tools

Once connected, Claude has access to:

| Tool | Description |
|------|-------------|
| `get_whatsapp_status` | Check connection status and account count |
| `send_whatsapp_message` | Send a message to any phone number |
| `read_whatsapp_messages` | Read recent messages (all or filtered by chat) |
| `list_whatsapp_contacts` | Search and list contacts |

## Architecture

```
┌─────────────────┐     stdio JSON-RPC     ┌────────────────┐
│  Claude Desktop  │◄─────────────────────►│ claude-bridge   │
│  or Claude Code  │                        │   --mcp         │
└─────────────────┘                        └──────┬─────────┘
                                                   │ HTTP proxy
┌─────────────────┐     HTTP :10002        ┌──────▼─────────┐
│  Web Dashboard   │◄─────────────────────►│ Claude Bridge   │
│  (browser)       │                        │ HTTP Server     │
└─────────────────┘                        └──────┬─────────┘
                                                   │
                          ┌──────────┼──────────┼──────────┼──────────┐
                          │          │          │          │          │
                   ┌──────▼───┐ ┌────▼────┐ ┌──▼───┐ ┌───▼────┐ ┌───▼────┐
                   │ WhatsApp │ │Xiao Hong│ │Linke-│ │Insta-  │ │Facebook│
                   │(whatsmeow│ │  Shu    │ │ dIn  │ │ gram   │ │ Pages  │
                   └──────────┘ └─────────┘ └──────┘ └────────┘ └────────┘
```

## Data storage

All data stays local in `~/.claude-bridge/`:

- `app.db` — Account list (SQLite)
- `whatsapp/whatsapp.db` — WhatsApp sessions (SQLite, managed by whatsmeow)
- `cert.pem` / `key.pem` — Auto-generated TLS certificate

Each device gets a fresh database — nothing is shared or synced.

## Requirements

- Go 1.22+
- C compiler (for SQLite via cgo)
  - macOS: `xcode-select --install`
  - Windows: [TDM-GCC](https://jmeubank.github.io/tdm-gcc/) or MSYS2
  - Linux: `sudo apt install build-essential`
- Chrome or Chromium (for browser-based connectors: Facebook, Instagram, LinkedIn, Xiao Hong Shu). If not installed, Claude Bridge will auto-download Chromium (~150MB) on first use.

See [SETUP.md](SETUP.md) for detailed build and configuration instructions.

## Roadmap

- [x] WhatsApp connector (personal mode, QR scan, multi-account)
- [x] MCP server with Claude Desktop one-click install
- [x] Web dashboard with health checks
- [ ] Xiao Hong Shu (create and manage posts)
- [ ] LinkedIn (create posts, manage content)
- [ ] Instagram (create posts, manage content)
- [ ] Facebook Pages (create posts, schedule content)
- [ ] Persistent message storage

## License

MIT — see [LICENSE](LICENSE)
