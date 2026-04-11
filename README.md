# CRM Agent

A single-binary CRM tool for insurance agents. Connect your personal WhatsApp account and let Claude manage your conversations through MCP (Model Context Protocol).

## What it does

- **WhatsApp connector** — Link your personal WhatsApp via QR code scan (uses [whatsmeow](https://github.com/tulir/whatsmeow))
- **Multi-account support** — Connect multiple WhatsApp numbers, each with reconnect/disconnect controls
- **Claude Desktop integration** — One-click install adds CRM Agent as an MCP server so Claude can read and send WhatsApp messages
- **Web dashboard** — Manage accounts, view status, and configure everything from your browser
- **System tray** — Runs in the background with a menu bar icon (macOS) or notification area (Windows)
- **Single binary** — No external dependencies at runtime. Everything (UI, API, MCP server) is embedded in one Go binary

## Quick start

```bash
# Build (requires Go 1.22+ and a C compiler for SQLite)
CGO_ENABLED=1 go build -o crm-agent .

# Run
./crm-agent
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
    "crm-agent": {
      "command": "/path/to/crm-agent",
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
┌─────────────────┐     stdio JSON-RPC     ┌──────────────┐
│  Claude Desktop  │◄─────────────────────►│ crm-agent    │
│  or Claude Code  │                        │   --mcp      │
└─────────────────┘                        └──────┬───────┘
                                                   │ HTTP proxy
┌─────────────────┐     HTTP :10002        ┌──────▼───────┐
│  Web Dashboard   │◄─────────────────────►│  CRM Agent   │
│  (browser)       │                        │  HTTP Server │
└─────────────────┘                        └──────┬───────┘
                                                   │
                                           ┌──────▼───────┐
                                           │  WhatsApp    │
                                           │  (whatsmeow) │
                                           └──────────────┘
```

Everything runs locally. No data leaves your machine except WhatsApp messages to WhatsApp's servers.

## Data storage

All data stays in `~/.crm-agent/`:

- `app.db` — Account list (SQLite)
- `whatsapp/whatsapp.db` — WhatsApp sessions (SQLite, managed by whatsmeow)
- `cert.pem` / `key.pem` — Auto-generated TLS certificate

Messages are stored in memory only for now. Persistent message storage is planned for Phase 2.

## Requirements

- Go 1.22+
- C compiler (for SQLite via cgo)
  - macOS: `xcode-select --install`
  - Windows: [TDM-GCC](https://jmeubank.github.io/tdm-gcc/) or MSYS2
  - Linux: `sudo apt install build-essential`

See [SETUP.md](SETUP.md) for detailed build and configuration instructions.

## License

MIT
