package server

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// handleBroadcastProgress serves the live broadcast progress page at /broadcasts/{id}.
// The page consumes the SSE stream at /api/batch/events?batch_id={id}.
func (s *Server) handleBroadcastProgress(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/broadcasts/")
	if id == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	safeID := html.EscapeString(id)
	fmt.Fprintf(w, broadcastHTML, safeID, safeID)
}

// broadcastHTML is the live progress page template. The two %s slots are
// the batch ID — once for the <title> and once for the visible label.
//
// IMPORTANT: This template uses Go's fmt.Fprintf, so any literal '%' character
// in the body must be doubled to '%%' (the percent signs in CSS widths and
// the JS toFixed call are doubled below).
const broadcastHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Broadcast %s — Claude Bridge</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
<style>
.bar { width: 100%%; height: 24px; background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; overflow: hidden; }
.bar-fill { height: 100%%; background: var(--accent); transition: width 0.4s ease; }
.stat-row { display: flex; gap: 16px; margin: 16px 0; }
.stat { background: var(--bg-card); border: 1px solid var(--border); border-radius: 8px; padding: 12px 16px; flex: 1; text-align: center; }
.stat .v { font-size: 22px; font-weight: 700; }
.stat .l { font-size: 12px; color: var(--text-muted); text-transform: uppercase; }
#log { font-family: monospace; font-size: 12px; max-height: 320px; overflow-y: auto; background: var(--code-bg); padding: 12px; border-radius: 8px; }
.row-fail { color: #c00; }
.row-ok { color: #080; }
button { margin-top: 12px; padding: 8px 16px; }
</style>
</head>
<body>
<nav class="topnav">
	<div class="logo">Claude <span>Bridge</span></div>
	<a href="/">Dashboard</a>
	<a href="/setup/whatsapp">WhatsApp</a>
	<a href="/contacts">Contacts</a>
	<div class="spacer"></div>
	<button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle theme"></button>
</nav>
<div class="container narrow">
	<h1>Broadcast Progress</h1>
	<p style="color: var(--text-muted)">Batch ID: <code>%s</code></p>

	<div class="bar"><div class="bar-fill" id="bar" style="width: 0%%"></div></div>
	<div class="stat-row">
		<div class="stat"><div class="v" id="sent">0</div><div class="l">Sent</div></div>
		<div class="stat"><div class="v" id="failed">0</div><div class="l">Failed</div></div>
		<div class="stat"><div class="v" id="total">0</div><div class="l">Total</div></div>
		<div class="stat"><div class="v" id="status">running</div><div class="l">Status</div></div>
	</div>
	<button onclick="cancelBatch()">Cancel</button>
	<h3 style="margin-top:24px">Activity Log</h3>
	<div id="log"></div>
</div>
<script>
const batchId = location.pathname.split('/').pop();
const bar = document.getElementById('bar');
const sentEl = document.getElementById('sent');
const failedEl = document.getElementById('failed');
const totalEl = document.getElementById('total');
const statusEl = document.getElementById('status');
const logEl = document.getElementById('log');
let failed = 0;

const es = new EventSource('/api/batch/events?batch_id=' + encodeURIComponent(batchId));
es.onmessage = (ev) => {
	const u = JSON.parse(ev.data);
	if (u.total) totalEl.textContent = u.total;
	if (u.progress !== undefined) {
		sentEl.textContent = u.progress;
		const pct = u.total ? (100 * u.progress / u.total) : 0;
		bar.style.width = pct.toFixed(1) + '%%';
	}
	if (u.status === 'failed') {
		failed++;
		failedEl.textContent = failed;
	}
	if (u.status && (u.status === 'completed' && !u.job_id)) statusEl.textContent = 'done';
	const row = document.createElement('div');
	row.className = u.status === 'failed' ? 'row-fail' : 'row-ok';
	const t = new Date().toLocaleTimeString();
	row.textContent = '[' + t + '] job ' + (u.job_id || '-') + ' ' + (u.status || '') + (u.error ? ' — ' + u.error : (u.note ? ' — ' + u.note : ''));
	logEl.prepend(row);
};
es.onerror = () => { statusEl.textContent = 'disconnected'; };

async function cancelBatch() {
	if (!confirm('Cancel remaining sends?')) return;
	const res = await fetch('/api/batch/cancel', {method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({batch_id: batchId})});
	if (res.ok) {
		statusEl.textContent = 'cancelled';
	} else {
		statusEl.textContent = 'cancel failed';
		console.error('cancel failed:', res.status);
	}
}
</script>
</body>
</html>`
