package server

const facebookHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Claude Bridge — Facebook</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
<style>
.fb-section { margin-bottom: 32px; }
.fb-section h3 { margin-bottom: 16px; }
.status-row { display:flex; align-items:center; gap:12px; margin-bottom:16px; }
.status-row .name { font-weight:600; font-size:1.05em; }
.cred-saved { color: var(--color-success); font-size: 0.85em; }
.btn-row { display:flex; gap:12px; margin-top:16px; }
.msg-list { max-height:300px; overflow-y:auto; border:1px solid var(--border); border-radius:8px; padding:12px; margin-top:12px; }
.msg-item { padding:8px 0; border-bottom:1px solid var(--border); }
.msg-item:last-child { border-bottom:none; }
.msg-sender { font-weight:600; font-size:0.85em; color: var(--text-secondary); }
.msg-content { margin-top:4px; }
.msg-outgoing { text-align:right; }
.cache-info { font-size:0.8em; color: var(--text-secondary); margin-bottom:8px; font-style: italic; }
.health-status { display:inline-flex; align-items:center; gap:6px; font-size:0.85em; padding:4px 10px; border-radius:12px; }
.health-ok { background: var(--color-success-bg, #e6f4ea); color: var(--color-success, #1a7f37); }
.health-fail { background: var(--color-error-bg, #fde8e8); color: var(--color-error, #d32f2f); }
.health-unknown { background: var(--bg-secondary); color: var(--text-secondary); }
.password-field { position:relative; }
.password-field input { padding-right:40px; }
.password-toggle { position:absolute; right:8px; top:50%; transform:translateY(-50%); background:none; border:none; cursor:pointer; color:var(--text-secondary); font-size:1.1em; }
</style>
</head>
<body>
<nav class="topnav">
	<div class="logo">Claude <span>Bridge</span></div>
	<a href="/">Dashboard</a>
	<a href="/setup/whatsapp">WhatsApp</a>
	<a href="/setup/facebook" class="active">Facebook</a>
	<div class="spacer"></div>
	<button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle light/dark theme"></button>
</nav>

<div class="container narrow">
	<h1 class="page-title">Facebook</h1>
	<p class="page-subtitle">Connect your Facebook account for posting and Messenger.</p>

	<!-- Browser not ready banner -->
	<div class="card fb-section" id="browserNotReady" style="display:none;border-left:4px solid var(--color-warning, #f59e0b);">
		<div style="display:flex;align-items:center;gap:16px;">
			<div class="health-spinner" style="width:24px;height:24px;border-width:3px;flex-shrink:0;"></div>
			<div>
				<strong id="browserBannerTitle">Browser engine is setting up...</strong>
				<div style="font-size:13px;color:var(--text-secondary);margin-top:4px;" id="browserBannerMsg">
					Facebook requires browser automation. Please wait while the browser engine is prepared.
				</div>
			</div>
		</div>
	</div>

	<!-- Connection Status -->
	<div class="card fb-section" id="connectionSection">
		<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px;">
			<h3>Connection</h3>
			<div>
				<span class="status-badge disconnected" id="fbBadge">Not Connected</span>
				<span class="health-status health-unknown" id="healthBadge" style="margin-left:8px;">Health: —</span>
			</div>
		</div>

		<div id="connectedInfo" style="display:none;">
			<div class="status-row">
				<span class="name" id="userName">—</span>
				<span class="cred-saved" id="credInfo"></span>
			</div>
			<div class="btn-row">
				<button class="btn btn-secondary" onclick="reconnect()">Reconnect</button>
				<button class="btn btn-danger" onclick="disconnect()">Disconnect</button>
				<button class="btn btn-secondary" onclick="runHealthCheck()">Run Health Check</button>
			</div>
		</div>

		<div id="loginForm">
			<div class="form-group">
				<label>Email / Phone</label>
				<input type="text" id="fbEmail" placeholder="your@email.com or phone number" autocomplete="username">
			</div>
			<div class="form-group">
				<label>Password</label>
				<div class="password-field">
					<input type="password" id="fbPassword" placeholder="Your Facebook password" autocomplete="current-password">
					<button class="password-toggle" onclick="togglePassword()" id="pwToggle">👁</button>
				</div>
				<div class="hint">Stored locally only — never leaves your machine. Used by the browser bot to log in on your behalf.</div>
			</div>
			<div class="btn-row">
				<button class="btn btn-primary" onclick="saveAndLogin()">Save & Connect</button>
			</div>
		</div>
	</div>

	<!-- Browser Settings -->
	<div class="card fb-section">
		<div style="display:flex;justify-content:space-between;align-items:center;">
			<h3>Browser Mode</h3>
			<label style="display:flex;align-items:center;gap:8px;cursor:pointer;">
				<input type="checkbox" id="headlessToggle" onchange="toggleHeadless()" checked>
				Headless (invisible)
			</label>
		</div>
		<div class="hint" style="margin-top:8px;">
			When headless is off, you can see the browser window performing actions. Useful for debugging or verifying login.
		</div>
	</div>

	<!-- Messenger API (Graph API) -->
	<div class="card fb-section" id="messengerSection">
		<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:16px;">
			<h3>Messenger (Graph API)</h3>
			<span class="status-badge disconnected" id="messengerBadge">Not Connected</span>
		</div>
		<div class="hint" style="margin-bottom:16px;">
			Messenger uses the official Facebook Graph API for reading and sending messages. You need to create a Meta App first —
			<a href="https://developers.facebook.com/apps/" target="_blank" rel="noopener">developers.facebook.com/apps</a>
		</div>

		<div id="messengerSetupForm">
			<div class="form-group">
				<label>Meta App ID</label>
				<input type="text" id="messengerAppId" placeholder="Your Meta App ID">
			</div>
			<div class="form-group">
				<label>App Secret</label>
				<div class="password-field">
					<input type="password" id="messengerAppSecret" placeholder="Your Meta App Secret">
					<button class="password-toggle" onclick="toggleSecret()" id="secretToggle">👁</button>
				</div>
			</div>
			<div class="btn-row">
				<button class="btn btn-primary" onclick="saveMessengerOAuth()">Save & Connect</button>
			</div>
		</div>

		<div id="messengerConnected" style="display:none;">
			<div class="status-row">
				<span class="name" id="messengerPageName">—</span>
				<span class="cred-saved" id="messengerPageId"></span>
			</div>
			<div style="display:flex;align-items:center;gap:16px;margin:12px 0;">
				<label style="display:flex;align-items:center;gap:8px;cursor:pointer;">
					<input type="checkbox" id="pollingToggle" onchange="togglePolling()">
					Auto-poll for new messages (every 30s)
				</label>
				<span id="pollingStatus" style="font-size:0.8em;color:var(--text-secondary);"></span>
			</div>
			<div class="btn-row">
				<button class="btn btn-secondary" onclick="refreshConversations()">Refresh Conversations</button>
				<button class="btn btn-danger" onclick="disconnectMessenger()">Disconnect Messenger</button>
			</div>
		</div>
	</div>

	<!-- Quick Actions -->
	<div class="card fb-section" id="actionsSection" style="display:none;">
		<h3>Quick Actions</h3>
		<div class="btn-row" style="flex-wrap:wrap;">
			<button class="btn btn-primary" onclick="showPostForm()">Create Post</button>
			<button class="btn btn-secondary" onclick="refreshContacts()">Refresh Contacts</button>
			<button class="btn btn-secondary" onclick="runHealthCheck()">Health Check</button>
		</div>

		<!-- Post Form (hidden by default) -->
		<div id="postForm" style="display:none;margin-top:20px;">
			<div class="form-group">
				<label>Post Content</label>
				<textarea id="postContent" rows="4" style="width:100%;padding:10px;border:1px solid var(--border);border-radius:8px;font-family:inherit;resize:vertical;background:var(--bg-primary);color:var(--text-primary);"></textarea>
			</div>
			<div class="form-group">
				<label>Page URL (optional)</label>
				<input type="text" id="postPageUrl" placeholder="https://www.facebook.com/YourPageName">
				<div class="hint">Leave empty to post to your personal timeline</div>
			</div>
			<div class="btn-row">
				<button class="btn btn-primary" onclick="createPost()">Post</button>
				<button class="btn btn-secondary" onclick="document.getElementById('postForm').style.display='none'">Cancel</button>
			</div>
		</div>
	</div>

	<!-- Conversations (from Messenger API) -->
	<div class="card fb-section" id="contactsSection" style="display:none;">
		<div style="display:flex;justify-content:space-between;align-items:center;">
			<h3>Messenger Conversations</h3>
			<span class="cache-info" id="contactsCacheInfo"></span>
		</div>
		<div id="contactsList" class="msg-list">
			<div style="color:var(--text-secondary);text-align:center;padding:20px;">No conversations yet. Connect Messenger and click "Refresh Conversations".</div>
		</div>
	</div>
</div>

<script>
let isConnected = false;

function togglePassword() {
	const pw = document.getElementById('fbPassword');
	const btn = document.getElementById('pwToggle');
	if (pw.type === 'password') {
		pw.type = 'text';
		btn.textContent = '🙈';
	} else {
		pw.type = 'password';
		btn.textContent = '👁';
	}
}

function saveAndLogin() {
	const email = document.getElementById('fbEmail').value.trim();
	const password = document.getElementById('fbPassword').value;
	if (!email || !password) { alert('Please enter both email/phone and password.'); return; }

	// First save credentials
	fetch('/api/facebook/credentials', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ email, password })
	})
	.then(r => r.json())
	.then(data => {
		if (!data.ok) { alert('Failed to save credentials: ' + (data.error || 'unknown')); return; }
		// Then trigger login
		document.getElementById('fbBadge').textContent = 'Connecting...';
		document.getElementById('fbBadge').className = 'status-badge';
		return fetch('/api/facebook/login', { method: 'POST' });
	})
	.then(r => r && r.json())
	.then(data => {
		if (!data) return;
		if (data.ok) {
			updateConnected(true, data.user_name);
		} else {
			alert('Login failed: ' + (data.error || 'unknown'));
			updateConnected(false);
		}
	})
	.catch(err => { alert('Error: ' + err.message); updateConnected(false); });
}

function reconnect() {
	document.getElementById('fbBadge').textContent = 'Connecting...';
	fetch('/api/facebook/login', { method: 'POST' })
	.then(r => r.json())
	.then(data => {
		if (data.ok) {
			updateConnected(true, data.user_name);
		} else {
			alert('Reconnect failed: ' + (data.error || 'unknown'));
		}
	})
	.catch(err => alert('Error: ' + err.message));
}

function disconnect() {
	if (!confirm('Disconnect Facebook?')) return;
	fetch('/api/facebook/disconnect', { method: 'POST' })
	.then(r => r.json())
	.then(() => updateConnected(false));
}

function updateConnected(connected, userName) {
	isConnected = connected;
	const badge = document.getElementById('fbBadge');
	const loginForm = document.getElementById('loginForm');
	const connectedInfo = document.getElementById('connectedInfo');
	const actionsSection = document.getElementById('actionsSection');
	const contactsSection = document.getElementById('contactsSection');

	if (connected) {
		badge.className = 'status-badge connected';
		badge.textContent = 'Connected';
		loginForm.style.display = 'none';
		connectedInfo.style.display = 'block';
		actionsSection.style.display = 'block';
		contactsSection.style.display = 'block';
		if (userName) document.getElementById('userName').textContent = userName;
	} else {
		badge.className = 'status-badge disconnected';
		badge.textContent = 'Not Connected';
		loginForm.style.display = 'block';
		connectedInfo.style.display = 'none';
		actionsSection.style.display = 'none';
		contactsSection.style.display = 'none';
	}
}

function toggleHeadless() {
	const headless = document.getElementById('headlessToggle').checked;
	fetch('/api/browser/headless', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ headless })
	});
}

function showPostForm() {
	document.getElementById('postForm').style.display = 'block';
}

function createPost() {
	const content = document.getElementById('postContent').value.trim();
	if (!content) { alert('Please enter post content.'); return; }
	const pageUrl = document.getElementById('postPageUrl').value.trim();

	fetch('/api/facebook/post', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ content, page_url: pageUrl || undefined })
	})
	.then(r => r.json())
	.then(data => {
		if (data.ok) {
			alert('Post created successfully!');
			document.getElementById('postContent').value = '';
			document.getElementById('postForm').style.display = 'none';
		} else {
			alert('Failed: ' + (data.error || 'unknown'));
		}
	})
	.catch(err => alert('Error: ' + err.message));
}

function refreshContacts() {
	refreshConversations();
}

function refreshConversations() {
	document.getElementById('contactsList').innerHTML = '<div style="text-align:center;padding:20px;">Fetching conversations...</div>';
	fetch('/api/facebook/messenger/conversations?refresh=true')
	.then(r => r.json())
	.then(data => {
		if (data.ok) {
			renderContacts(data.conversations || [], data.synced_at);
		} else {
			document.getElementById('contactsList').innerHTML = '<div style="color:var(--color-error);padding:20px;">Error: ' + (data.error || 'unknown') + '</div>';
		}
	})
	.catch(err => {
		document.getElementById('contactsList').innerHTML = '<div style="color:var(--color-error);padding:20px;">Error: ' + err.message + '</div>';
	});
}

// --- Messenger OAuth ---

function toggleSecret() {
	const pw = document.getElementById('messengerAppSecret');
	const btn = document.getElementById('secretToggle');
	if (pw.type === 'password') { pw.type = 'text'; btn.textContent = '🙈'; }
	else { pw.type = 'password'; btn.textContent = '👁'; }
}

function saveMessengerOAuth() {
	const appId = document.getElementById('messengerAppId').value.trim();
	const appSecret = document.getElementById('messengerAppSecret').value.trim();
	if (!appId || !appSecret) { alert('Please enter both App ID and App Secret.'); return; }

	fetch('/api/facebook/messenger/oauth/config', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ app_id: appId, app_secret: appSecret })
	})
	.then(r => r.json())
	.then(data => {
		if (!data.ok) { alert('Failed: ' + (data.error || 'unknown')); return; }
		// Now start OAuth flow
		return fetch('/api/facebook/messenger/oauth/start').then(r => r.json());
	})
	.then(data => {
		if (!data) return;
		if (data.ok && data.url) {
			window.open(data.url, '_blank', 'width=600,height=700');
			// Poll for connection
			var oauthPoll = setInterval(function() {
				fetch('/api/facebook/messenger/status').then(r => r.json()).then(status => {
					if (status.connected) {
						clearInterval(oauthPoll);
						updateMessengerStatus(status);
					}
				});
			}, 2000);
			// Stop polling after 5 minutes
			setTimeout(function() { clearInterval(oauthPoll); }, 300000);
		} else {
			alert('Failed to start OAuth: ' + (data.error || 'unknown'));
		}
	})
	.catch(err => alert('Error: ' + err.message));
}

function updateMessengerStatus(status) {
	const badge = document.getElementById('messengerBadge');
	const setupForm = document.getElementById('messengerSetupForm');
	const connectedDiv = document.getElementById('messengerConnected');
	const contactsSection = document.getElementById('contactsSection');

	if (status.connected) {
		badge.className = 'status-badge connected';
		badge.textContent = 'Connected';
		setupForm.style.display = 'none';
		connectedDiv.style.display = 'block';
		contactsSection.style.display = 'block';
		if (status.page_name) document.getElementById('messengerPageName').textContent = status.page_name;
		if (status.page_id) document.getElementById('messengerPageId').textContent = 'Page ID: ' + status.page_id;
		document.getElementById('pollingToggle').checked = !!status.polling;
		document.getElementById('pollingStatus').textContent = status.polling ? 'Polling active' : '';
	} else {
		badge.className = 'status-badge disconnected';
		badge.textContent = 'Not Connected';
		setupForm.style.display = 'block';
		connectedDiv.style.display = 'none';
	}
}

function togglePolling() {
	const enabled = document.getElementById('pollingToggle').checked;
	fetch('/api/facebook/messenger/polling', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ enabled })
	})
	.then(r => r.json())
	.then(data => {
		document.getElementById('pollingStatus').textContent = data.polling ? 'Polling active' : '';
	});
}

function disconnectMessenger() {
	if (!confirm('Disconnect Facebook Messenger? You will need to re-authorize.')) return;
	fetch('/api/facebook/messenger/disconnect', { method: 'POST' })
	.then(r => r.json())
	.then(() => updateMessengerStatus({ connected: false }));
}

function renderContacts(contacts, syncedAt) {
	const list = document.getElementById('contactsList');
	if (syncedAt) {
		document.getElementById('contactsCacheInfo').textContent = 'Last synced: ' + new Date(syncedAt).toLocaleString();
	}
	if (!contacts || contacts.length === 0) {
		list.innerHTML = '<div style="color:var(--text-secondary);text-align:center;padding:20px;">No contacts found.</div>';
		return;
	}
	list.innerHTML = contacts.map(c =>
		'<div class="msg-item"><div class="msg-sender">' + (c.name || c.contact_id) + '</div>' +
		(c.username ? '<div style="font-size:0.8em;color:var(--text-secondary);">@' + c.username + '</div>' : '') +
		'</div>'
	).join('');
}

function runHealthCheck() {
	const badge = document.getElementById('healthBadge');
	badge.className = 'health-status health-unknown';
	badge.textContent = 'Checking...';

	fetch('/api/facebook/healthcheck')
	.then(r => r.json())
	.then(data => {
		if (data.ok) {
			badge.className = 'health-status health-ok';
			badge.textContent = 'Health: OK';
		} else {
			badge.className = 'health-status health-fail';
			badge.textContent = 'Health: FAILED';
			alert('Health check failed: ' + (data.error || 'unknown'));
		}
	})
	.catch(err => {
		badge.className = 'health-status health-fail';
		badge.textContent = 'Health: ERROR';
	});
}

// --- Browser readiness check ---
var fbBrowserReady = false;

function checkBrowserStatus() {
	fetch('/api/browser/status')
	.then(r => r.json())
	.then(data => {
		const banner = document.getElementById('browserNotReady');
		const title = document.getElementById('browserBannerTitle');
		const msg = document.getElementById('browserBannerMsg');
		const connSection = document.getElementById('connectionSection');

		if (data.status === 'ready') {
			fbBrowserReady = true;
			banner.style.display = 'none';
			connSection.style.opacity = '1';
			connSection.style.pointerEvents = 'auto';
			document.querySelectorAll('.fb-section:not(#browserNotReady)').forEach(el => {
				el.style.opacity = '1';
				el.style.pointerEvents = 'auto';
			});
		} else {
			fbBrowserReady = false;
			banner.style.display = '';
			// Disable all other sections
			document.querySelectorAll('.fb-section:not(#browserNotReady)').forEach(el => {
				el.style.opacity = '0.4';
				el.style.pointerEvents = 'none';
			});

			if (data.status === 'downloading') {
				title.textContent = 'Downloading browser engine...';
				msg.textContent = data.message || 'Downloading Chromium (~150MB, first time only). This only happens once.';
			} else if (data.status === 'error') {
				title.textContent = 'Browser setup failed';
				msg.textContent = data.error || 'Please restart Claude Bridge to try again.';
			} else {
				title.textContent = 'Browser engine is setting up...';
				msg.textContent = 'Checking for Chrome/Chromium on your system.';
			}
		}
	});
}

checkBrowserStatus();
var browserPollInterval = setInterval(function() {
	if (fbBrowserReady) { clearInterval(browserPollInterval); return; }
	checkBrowserStatus();
}, 2000);

// Initial load
fetch('/api/facebook/status')
.then(r => r.json())
.then(data => {
	updateConnected(data.connected, data.user_name);
});

fetch('/api/facebook/credentials')
.then(r => r.json())
.then(data => {
	if (data.saved) {
		document.getElementById('fbEmail').value = data.email;
		document.getElementById('credInfo').textContent = '(credentials saved)';
	}
});

fetch('/api/browser/headless')
.then(r => r.json())
.then(data => {
	document.getElementById('headlessToggle').checked = data.headless;
});

// Load Messenger status
fetch('/api/facebook/messenger/status')
.then(r => r.json())
.then(status => updateMessengerStatus(status));

// Load saved OAuth config (to pre-fill App ID)
fetch('/api/facebook/messenger/oauth/config')
.then(r => r.json())
.then(data => {
	if (data.configured && data.app_id) {
		document.getElementById('messengerAppId').value = data.app_id;
	}
});

// Load cached conversations
fetch('/api/facebook/messenger/conversations')
.then(r => r.json())
.then(data => {
	if (data.ok && data.conversations && data.conversations.length > 0) {
		renderContacts(data.conversations, data.synced_at);
	}
});
</script>
</body>
</html>`
