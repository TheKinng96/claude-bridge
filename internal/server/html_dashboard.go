package server

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Claude Bridge — Dashboard</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
<style>
/* Dashboard-specific */
.status-bar { display: flex; gap: 16px; margin-bottom: 32px; flex-wrap: wrap; }
.status-pill { display: flex; align-items: center; gap: 8px; background: var(--bg-card); border: 1px solid var(--border); border-radius: 8px; padding: 12px 20px; font-size: 14px; box-shadow: var(--shadow); }

.cards { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 20px; margin-bottom: 32px; }
.card h3 { font-size: 14px; color: var(--text-muted); font-weight: 500; margin-bottom: 8px; text-transform: uppercase; letter-spacing: 0.5px; }
.card .value { font-size: 32px; font-weight: 700; color: var(--text); }
.card .sub { font-size: 13px; color: var(--text-dim); margin-top: 4px; }

.section-title { font-size: 18px; font-weight: 600; color: var(--text); margin-bottom: 16px; display: flex; align-items: center; gap: 8px; }

.connector-card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 24px; margin-bottom: 16px; display: flex; justify-content: space-between; align-items: center; box-shadow: var(--shadow); }
.connector-info { display: flex; align-items: center; gap: 16px; }
.connector-icon { width: 48px; height: 48px; border-radius: 12px; display: flex; align-items: center; justify-content: center; font-size: 24px; }
.connector-icon.wa { background: var(--green-bg); }
.connector-icon.fb { background: #3b82f620; }
.connector-icon.cl { background: #d4a57420; }
.connector-details h4 { font-size: 16px; color: var(--text); margin-bottom: 4px; }
.connector-details p { font-size: 13px; color: var(--text-dim); }

.claude-card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 24px; margin-bottom: 16px; box-shadow: var(--shadow); }

/* Guide panel */
.guide-panel { display: none; background: var(--bg); border: 1px solid var(--border); border-radius: 12px; padding: 24px; margin-top: 12px; margin-bottom: 16px; }
.guide-panel.open { display: block; }
.guide-panel h4 { font-size: 15px; color: var(--text); margin-bottom: 16px; }
.guide-step { display: flex; gap: 12px; margin-bottom: 16px; }
.guide-step:last-child { margin-bottom: 0; }
.guide-num { width: 24px; height: 24px; border-radius: 50%; background: var(--accent); color: #fff; font-size: 12px; font-weight: 700; display: flex; align-items: center; justify-content: center; flex-shrink: 0; margin-top: 1px; }
.guide-text { font-size: 14px; color: var(--text); line-height: 1.5; }
.guide-text strong { color: var(--text); }
.copy-row { display: flex; align-items: center; gap: 8px; margin-top: 8px; }
.copy-field { flex: 1; background: var(--code-bg); border: 1px solid var(--border); border-radius: 6px; padding: 8px 12px; font-family: 'SF Mono', Monaco, monospace; font-size: 13px; color: var(--code-color); user-select: all; overflow-x: auto; white-space: nowrap; }
.copy-btn { background: var(--bg-card); border: 1px solid var(--border); color: var(--text-muted); border-radius: 6px; padding: 8px 12px; font-size: 12px; font-weight: 500; cursor: pointer; white-space: nowrap; transition: all 0.2s; }
.copy-btn:hover { background: var(--accent); color: #fff; border-color: var(--accent); }
.copy-btn.copied { background: var(--green-bg); color: var(--green); border-color: var(--green-border); }
.guide-field-label { font-size: 12px; color: var(--text-dim); margin-top: 10px; margin-bottom: 2px; text-transform: uppercase; letter-spacing: 0.5px; }
.guide-note { font-size: 12px; color: var(--text-dim); font-style: italic; margin-top: 4px; }

/* Activity feed */
.activity-feed { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; box-shadow: var(--shadow); }
.activity-item { padding: 16px 24px; border-bottom: 1px solid var(--border); display: flex; gap: 12px; align-items: flex-start; }
.activity-item:last-child { border-bottom: none; }
.activity-dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); margin-top: 6px; flex-shrink: 0; }
.activity-text { font-size: 14px; color: var(--text); }
.activity-time { font-size: 12px; color: var(--text-dim); margin-top: 2px; }
.empty-state { padding: 48px; text-align: center; color: var(--text-dim); font-size: 14px; }

/* Browser setup banner */
.browser-setup-banner { background: var(--bg-card); border: 1px solid var(--accent); border-radius: 12px; padding: 20px 24px; margin-bottom: 24px; box-shadow: var(--shadow); }
.browser-setup-banner.error { border-color: var(--red, #dc3545); }
.browser-setup-banner.ready { border-color: var(--green, #28a745); }
.browser-setup-inner { display: flex; align-items: center; gap: 16px; }
.browser-setup-icon { flex-shrink: 0; }
.browser-setup-text strong { font-size: 15px; color: var(--text); }
.browser-progress-bar { margin-top: 16px; height: 6px; background: var(--border); border-radius: 3px; overflow: hidden; }
.browser-progress-fill { height: 100%; background: var(--accent); border-radius: 3px; transition: width 0.5s ease; animation: indeterminate 1.5s ease-in-out infinite; width: 40%; }
@keyframes indeterminate { 0% { margin-left: 0; width: 30%; } 50% { margin-left: 30%; width: 40%; } 100% { margin-left: 70%; width: 30%; } }

/* Disabled connector overlay */
.connector-card.disabled { opacity: 0.5; pointer-events: none; position: relative; }
.connector-card.disabled::after { content: 'Waiting for automation bot...'; position: absolute; right: 24px; font-size: 12px; color: var(--text-dim); font-style: italic; }

/* Health check overlay */
.health-overlay { position: fixed; top: 0; left: 0; right: 0; bottom: 0; background: rgba(0,0,0,0.35); display: flex; align-items: center; justify-content: center; z-index: 1000; backdrop-filter: blur(2px); }
.health-card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 16px; padding: 32px 40px; min-width: 340px; max-width: 440px; box-shadow: 0 20px 60px rgba(0,0,0,0.3); text-align: center; }
.health-card h3 { font-size: 16px; color: var(--text); margin-bottom: 20px; }
.health-checks { text-align: left; margin-bottom: 20px; }
.health-row { display: flex; align-items: center; gap: 12px; padding: 10px 0; border-bottom: 1px solid var(--border); font-size: 14px; color: var(--text); }
.health-row:last-child { border-bottom: none; }
.health-icon { width: 22px; text-align: center; font-size: 16px; flex-shrink: 0; }
.health-spinner { width: 16px; height: 16px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.6s linear infinite; display: inline-block; }
@keyframes spin { to { transform: rotate(360deg); } }
.health-label { flex: 1; }
.health-summary { font-size: 13px; color: var(--text-dim); margin-bottom: 16px; }
</style>
</head>
<body>
<nav class="topnav">
	<div class="logo">Claude <span>Bridge</span></div>
	<a href="/" class="active">Dashboard</a>
	<a href="/setup/whatsapp">WhatsApp</a>
	<a href="/setup/facebook">Facebook</a>
	<div class="spacer"></div>
	<button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle light/dark theme"></button>
</nav>

<div class="container">
	<!-- Browser Engine Setup Banner -->
	<div class="browser-setup-banner" id="browserBanner" style="display:none;">
		<div class="browser-setup-inner">
			<div class="browser-setup-icon"><div class="health-spinner" style="width:24px;height:24px;border-width:3px;"></div></div>
			<div class="browser-setup-text">
				<strong id="browserSetupTitle">Setting up automation bot...</strong>
				<div id="browserSetupMsg" style="font-size:13px;color:var(--text-dim);margin-top:4px;">Checking for Chrome/Chromium</div>
			</div>
		</div>
		<div class="browser-progress-bar" id="browserProgressBar" style="display:none;">
			<div class="browser-progress-fill" id="browserProgressFill"></div>
		</div>
	</div>

	<div class="status-bar" id="statusBar">
		<div class="status-pill">
			<span class="status-dot red" id="waDot"></span>
			<span id="waStatus">WhatsApp: Disconnected</span>
		</div>
		<div class="status-pill">
			<span class="status-dot red" id="fbDot"></span>
			<span id="fbStatus">Facebook: Disconnected</span>
		</div>
		<div class="status-pill" id="browserPill" style="display:none;">
			<span class="status-dot" id="browserDot"></span>
			<span id="browserStatusText">Browser: Checking</span>
		</div>
	</div>

	<div class="cards">
		<div class="card">
			<h3>Active Channels</h3>
			<div class="value" id="activeChannels">0</div>
			<div class="sub">of 2 configured</div>
		</div>
		<div class="card">
			<h3>Messages Today</h3>
			<div class="value" id="messageCount">0</div>
			<div class="sub">across all channels</div>
		</div>
		<div class="card">
			<h3>Contacts</h3>
			<div class="value" id="contactCount">0</div>
			<div class="sub">unique conversations</div>
		</div>
	</div>

	<h2 class="section-title">Channels</h2>
	<div class="connector-card">
		<div class="connector-info">
			<div class="connector-icon wa">💬</div>
			<div class="connector-details">
				<h4>WhatsApp</h4>
				<p id="waDetail">Not connected — scan QR to link</p>
			</div>
		</div>
		<a href="/setup/whatsapp" class="btn btn-primary" id="waBtn">Connect</a>
	</div>
	<div class="connector-card" id="fbCard">
		<div class="connector-info">
			<div class="connector-icon fb">📘</div>
			<div class="connector-details">
				<h4>Facebook</h4>
				<p id="fbDetail">Not connected</p>
			</div>
		</div>
		<a href="/setup/facebook" class="btn btn-outline" id="fbBtn">Setup</a>
	</div>

	<div class="claude-card" id="claudeCard">
		<div style="display:flex;justify-content:space-between;align-items:center;">
			<div class="connector-info">
				<div class="connector-icon cl">🤖</div>
				<div class="connector-details">
					<h4>Claude Desktop</h4>
					<p id="claudeDetail">Checking installation...</p>
				</div>
			</div>
			<div style="display:flex;gap:8px;align-items:center;">
				<span class="status-badge" id="claudeBadge" style="display:none;"></span>
				<button class="btn btn-primary" id="claudeInstallBtn" style="display:none;" onclick="installClaude()">Install to Claude</button>
				<button class="btn btn-outline" id="claudeUninstallBtn" style="display:none;" onclick="uninstallClaude()">Remove</button>
				<button class="btn btn-outline" onclick="toggleGuide()" id="guideToggle">Details</button>
			</div>
		</div>
	</div>

	<div class="guide-panel" id="guidePanel">
		<h4>How it works</h4>
		<div class="guide-step">
			<div class="guide-num">1</div>
			<div class="guide-text">
				Clicking <strong>Install to Claude</strong> automatically adds Claude Bridge to your Claude Desktop config file:
				<div class="copy-row" style="margin-top:8px;">
					<div class="copy-field" id="fieldConfigPath" style="font-size:12px;color:var(--text-dim);">Detecting path...</div>
				</div>
			</div>
		</div>
		<div class="guide-step">
			<div class="guide-num">2</div>
			<div class="guide-text"><strong>Restart Claude Desktop</strong> after installing to load the new MCP server.</div>
		</div>
		<div class="guide-step">
			<div class="guide-num">3</div>
			<div class="guide-text">
				Make sure Claude Bridge is running, then Claude has access to these tools:
				<div style="margin-top:8px;display:grid;grid-template-columns:1fr 1fr;gap:6px;">
					<div style="background:var(--code-bg);padding:8px 12px;border-radius:6px;font-size:13px;color:var(--text-muted);"><strong style="color:var(--accent);">get_whatsapp_status</strong> — check connection</div>
					<div style="background:var(--code-bg);padding:8px 12px;border-radius:6px;font-size:13px;color:var(--text-muted);"><strong style="color:var(--accent);">send_whatsapp_message</strong> — send to any number</div>
					<div style="background:var(--code-bg);padding:8px 12px;border-radius:6px;font-size:13px;color:var(--text-muted);"><strong style="color:var(--accent);">read_whatsapp_messages</strong> — read recent messages</div>
					<div style="background:var(--code-bg);padding:8px 12px;border-radius:6px;font-size:13px;color:var(--text-muted);"><strong style="color:var(--accent);">list_whatsapp_contacts</strong> — list all contacts</div>
				</div>
			</div>
		</div>
		<div class="guide-note">The MCP server proxies tool calls to the running HTTP server on port 10002. Claude Bridge must be running for Claude to use the tools.</div>
		<div class="guide-note" style="margin-top:8px;">For Claude Code, run: <code>claude-bridge --mcp</code> directly, or add the same config to your Claude Code MCP settings.</div>
	</div>

	<h2 class="section-title" style="margin-top:32px;">Recent Activity</h2>
	<div class="activity-feed" id="activityFeed">
		<div class="empty-state">No activity yet. Connect a channel to get started.</div>
	</div>
</div>

<!-- Health check overlay (shown once per day on load) -->
<div class="health-overlay" id="healthOverlay" style="display:none;">
	<div class="health-card">
		<h3>Running health check...</h3>
		<div class="health-checks" id="healthChecks"></div>
		<div class="health-summary" id="healthSummary"></div>
		<button class="btn btn-primary" id="healthDismiss" style="display:none;" onclick="dismissHealth()">Got it</button>
	</div>
</div>

<script>
var browserReady = false;

function pollStatus() {
	fetch('/api/status')
		.then(r => r.json())
		.then(data => {
			const wa = data.whatsapp || {};
			const fb = data.facebook || {};

			const waDot = document.getElementById('waDot');
			const waStatus = document.getElementById('waStatus');
			const waDetail = document.getElementById('waDetail');
			const waBtn = document.getElementById('waBtn');
			if (wa.connected) {
				waDot.className = 'status-dot green';
				waStatus.textContent = 'WhatsApp: ' + wa.connected_count + ' connected';
				waDetail.textContent = wa.connected_count + ' of ' + wa.total_accounts + ' accounts connected';
				waBtn.textContent = 'Manage';
				waBtn.className = 'btn btn-success';
			} else {
				waDot.className = 'status-dot red';
				waStatus.textContent = 'WhatsApp: Disconnected';
				waDetail.textContent = wa.total_accounts > 0 ? wa.total_accounts + ' accounts — none connected' : 'Not connected — scan QR to link';
				waBtn.textContent = 'Connect';
				waBtn.className = 'btn btn-primary';
			}

			const fbDot = document.getElementById('fbDot');
			const fbStatusEl = document.getElementById('fbStatus');
			const fbDetail = document.getElementById('fbDetail');
			const fbBtn = document.getElementById('fbBtn');
			if (fb.connected) {
				fbDot.className = 'status-dot green';
				fbStatusEl.textContent = 'Facebook: Connected';
				fbDetail.textContent = fb.user_name ? ('Logged in as ' + fb.user_name) : 'Connected';
				fbBtn.textContent = 'Manage';
				fbBtn.className = 'btn btn-success';
			} else {
				fbDot.className = 'status-dot red';
				fbStatusEl.textContent = 'Facebook: Disconnected';
				fbDetail.textContent = browserReady ? 'Not connected' : 'Waiting for automation bot...';
				fbBtn.textContent = 'Setup';
				fbBtn.className = 'btn btn-outline';
			}

			let active = 0;
			if (wa.connected) active++;
			if (fb.connected) active++;
			document.getElementById('activeChannels').textContent = active;
			document.getElementById('messageCount').textContent = data.message_count || 0;
			document.getElementById('contactCount').textContent = data.contact_count || 0;
		})
		.catch(() => {});
}

// --- Browser engine status ---
function pollBrowserStatus() {
	fetch('/api/browser/status')
		.then(r => r.json())
		.then(data => {
			const banner = document.getElementById('browserBanner');
			const title = document.getElementById('browserSetupTitle');
			const msg = document.getElementById('browserSetupMsg');
			const progressBar = document.getElementById('browserProgressBar');
			const pill = document.getElementById('browserPill');
			const dot = document.getElementById('browserDot');
			const dotText = document.getElementById('browserStatusText');
			const fbCard = document.getElementById('fbCard');

			pill.style.display = '';

			if (data.status === 'ready') {
				browserReady = true;
				banner.style.display = 'none';
				dot.className = 'status-dot green';
				dotText.textContent = 'Automation Bot: Ready';
				fbCard.classList.remove('disabled');
			} else if (data.status === 'downloading') {
				browserReady = false;
				banner.style.display = '';
				banner.className = 'browser-setup-banner';
				title.textContent = 'Setting up automation bot...';
				msg.textContent = data.message || 'Downloading automation components (~150MB, first time only)';
				progressBar.style.display = '';
				dot.className = 'status-dot yellow';
				dotText.textContent = 'Automation Bot: Setting up...';
				fbCard.classList.add('disabled');
			} else if (data.status === 'checking') {
				browserReady = false;
				banner.style.display = '';
				banner.className = 'browser-setup-banner';
				title.textContent = 'Setting up automation bot...';
				msg.textContent = data.message || 'Looking for Chrome/Chromium';
				progressBar.style.display = 'none';
				dot.className = 'status-dot yellow';
				dotText.textContent = 'Automation Bot: Checking...';
				fbCard.classList.add('disabled');
			} else if (data.status === 'error') {
				browserReady = false;
				banner.style.display = '';
				banner.className = 'browser-setup-banner error';
				title.textContent = 'Browser setup failed';
				msg.textContent = data.error || 'Unknown error';
				progressBar.style.display = 'none';
				dot.className = 'status-dot red';
				dotText.textContent = 'Automation Bot: Error';
				fbCard.classList.add('disabled');
			} else {
				// not_checked — trigger setup
				browserReady = false;
				banner.style.display = '';
				title.textContent = 'Initializing browser engine...';
				msg.textContent = 'Starting up...';
				progressBar.style.display = 'none';
				fbCard.classList.add('disabled');
				fetch('/api/browser/setup').then(r => r.json());
			}
		})
		.catch(() => {});
}

pollStatus();
pollBrowserStatus();
setInterval(pollStatus, 3000);
setInterval(pollBrowserStatus, 2000);

function toggleGuide() {
	const panel = document.getElementById('guidePanel');
	const btn = document.getElementById('guideToggle');
	if (panel.classList.contains('open')) {
		panel.classList.remove('open');
		btn.textContent = 'Details';
	} else {
		panel.classList.add('open');
		btn.textContent = 'Hide';
	}
}

function checkClaudeInstall() {
	fetch('/api/claude/status')
		.then(r => r.json())
		.then(data => {
			const detail = document.getElementById('claudeDetail');
			const badge = document.getElementById('claudeBadge');
			const installBtn = document.getElementById('claudeInstallBtn');
			const uninstallBtn = document.getElementById('claudeUninstallBtn');
			const configPathEl = document.getElementById('fieldConfigPath');

			if (!data.supported) {
				detail.textContent = 'Auto-install not supported on this OS';
				return;
			}

			if (configPathEl) configPathEl.textContent = data.config_path;

			if (data.installed) {
				detail.textContent = 'Installed — restart Claude Desktop if you just added it';
				badge.textContent = 'Installed';
				badge.className = 'status-badge connected';
				badge.style.display = '';
				installBtn.style.display = 'none';
				uninstallBtn.style.display = '';
			} else {
				detail.textContent = 'Let Claude read & send messages through your connected channels';
				badge.style.display = 'none';
				installBtn.style.display = '';
				uninstallBtn.style.display = 'none';
			}
		})
		.catch(() => {
			document.getElementById('claudeDetail').textContent = 'Let Claude read & send messages through your connected channels';
			document.getElementById('claudeInstallBtn').style.display = '';
		});
}

function installClaude() {
	const btn = document.getElementById('claudeInstallBtn');
	btn.disabled = true;
	btn.textContent = 'Installing...';

	fetch('/api/claude/install', { method: 'POST' })
		.then(r => r.json())
		.then(data => {
			if (data.ok) {
				checkClaudeInstall();
				// Open the guide panel to show "restart" reminder.
				const panel = document.getElementById('guidePanel');
				if (!panel.classList.contains('open')) toggleGuide();
			} else {
				alert('Install failed: ' + (data.error || 'unknown error'));
				btn.disabled = false;
				btn.textContent = 'Install to Claude';
			}
		})
		.catch(err => {
			alert('Error: ' + err.message);
			btn.disabled = false;
			btn.textContent = 'Install to Claude';
		});
}

function uninstallClaude() {
	if (!confirm('Remove Claude Bridge from Claude Desktop config?')) return;
	fetch('/api/claude/uninstall', { method: 'POST' })
		.then(r => r.json())
		.then(data => {
			if (data.ok) {
				checkClaudeInstall();
			} else {
				alert('Remove failed: ' + (data.error || 'unknown'));
			}
		})
		.catch(err => alert('Error: ' + err.message));
}

checkClaudeInstall();

// --- Health check (once per day) ---
var HEALTH_COOKIE = 'crm_health_checked';

function shouldRunHealthCheck() {
	var cookies = document.cookie.split(';');
	for (var i = 0; i < cookies.length; i++) {
		var c = cookies[i].trim();
		if (c.indexOf(HEALTH_COOKIE + '=') === 0) {
			var val = c.substring(HEALTH_COOKIE.length + 1);
			var lastCheck = parseInt(val, 10);
			// Check if less than 24 hours ago.
			if (Date.now() - lastCheck < 24 * 60 * 60 * 1000) return false;
		}
	}
	return true;
}

function markHealthChecked() {
	var expires = new Date(Date.now() + 24*60*60*1000).toUTCString();
	document.cookie = HEALTH_COOKIE + '=' + Date.now() + ';expires=' + expires + ';path=/;SameSite=Lax';
}

function runHealthCheck() {
	if (!shouldRunHealthCheck()) return;

	var overlay = document.getElementById('healthOverlay');
	var checksEl = document.getElementById('healthChecks');
	var summaryEl = document.getElementById('healthSummary');
	var dismissBtn = document.getElementById('healthDismiss');

	// Define checks.
	var checks = [
		{ id: 'http',    label: 'HTTP server',          status: 'checking' },
		{ id: 'browser', label: 'Browser engine',       status: 'checking' },
		{ id: 'wa',      label: 'WhatsApp accounts',    status: 'checking' },
		{ id: 'claude',  label: 'Claude Desktop config', status: 'checking' }
	];

	function render() {
		var html = '';
		for (var i = 0; i < checks.length; i++) {
			var c = checks[i];
			var icon = '';
			if (c.status === 'checking') icon = '<div class="health-spinner"></div>';
			else if (c.status === 'ok') icon = '<span style="color:var(--green);">&#10003;</span>';
			else if (c.status === 'warn') icon = '<span style="color:#f59e0b;">!</span>';
			else if (c.status === 'fail') icon = '<span style="color:var(--red);">&#10007;</span>';
			html += '<div class="health-row"><div class="health-icon">' + icon + '</div><div class="health-label">' + c.label + '</div></div>';
		}
		checksEl.innerHTML = html;
	}

	function setCheck(id, status, label) {
		for (var i = 0; i < checks.length; i++) {
			if (checks[i].id === id) {
				checks[i].status = status;
				if (label) checks[i].label = label;
			}
		}
		render();
	}

	function finish() {
		var issues = checks.filter(function(c) { return c.status === 'warn' || c.status === 'fail'; });
		if (issues.length === 0) {
			summaryEl.textContent = 'Everything looks good!';
			// Auto-dismiss after 1.5 seconds if all OK.
			markHealthChecked();
			setTimeout(function() { overlay.style.display = 'none'; }, 1500);
		} else {
			summaryEl.textContent = issues.length + ' item' + (issues.length > 1 ? 's' : '') + ' need' + (issues.length === 1 ? 's' : '') + ' attention.';
			dismissBtn.style.display = '';
			markHealthChecked();
		}
	}

	overlay.style.display = 'flex';
	render();

	var done = 0;
	var total = 4;
	function checkDone() { done++; if (done >= total) finish(); }

	// Check 1: HTTP server (we're already on it, so just confirm API responds).
	fetch('/api/status').then(function(r) { return r.json(); }).then(function(data) {
		setCheck('http', 'ok', 'HTTP server running');

		// Check 2: WhatsApp from same response.
		var wa = data.whatsapp || {};
		if (wa.connected) {
			setCheck('wa', 'ok', 'WhatsApp: ' + wa.connected_count + ' account' + (wa.connected_count > 1 ? 's' : '') + ' connected');
		} else if (wa.total_accounts > 0) {
			setCheck('wa', 'warn', 'WhatsApp: ' + wa.total_accounts + ' account' + (wa.total_accounts > 1 ? 's' : '') + ' saved but none connected');
		} else {
			setCheck('wa', 'warn', 'WhatsApp: no accounts — <a href="/setup/whatsapp" style="color:var(--accent);">add one</a>');
		}
		checkDone();
		checkDone();
	}).catch(function() {
		setCheck('http', 'fail', 'HTTP server not responding');
		setCheck('wa', 'fail', 'Cannot check — server unreachable');
		checkDone();
		checkDone();
	});

	// Check 3: Browser engine.
	fetch('/api/browser/status').then(function(r) { return r.json(); }).then(function(data) {
		if (data.status === 'ready') {
			setCheck('browser', 'ok', 'Automation bot: ready');
		} else if (data.status === 'downloading') {
			setCheck('browser', 'warn', 'Browser engine: downloading Chromium...');
		} else if (data.status === 'error') {
			setCheck('browser', 'fail', 'Browser engine: ' + (data.error || 'setup failed'));
		} else {
			setCheck('browser', 'warn', 'Browser engine: setting up...');
		}
		checkDone();
	}).catch(function() {
		setCheck('browser', 'warn', 'Browser engine: could not check');
		checkDone();
	});

	// Check 4: Claude Desktop config.
	fetch('/api/claude/status').then(function(r) { return r.json(); }).then(function(data) {
		if (!data.supported) {
			setCheck('claude', 'warn', 'Claude Desktop: auto-install not supported on this OS');
		} else if (data.installed) {
			setCheck('claude', 'ok', 'Claude Desktop: installed');
		} else {
			setCheck('claude', 'warn', 'Claude Desktop: not installed — click <strong>Install to Claude</strong> above');
		}
		checkDone();
	}).catch(function() {
		setCheck('claude', 'warn', 'Claude Desktop: could not check');
		checkDone();
	});
}

function dismissHealth() {
	document.getElementById('healthOverlay').style.display = 'none';
}

// Run on load.
runHealthCheck();
</script>
</body>
</html>`
