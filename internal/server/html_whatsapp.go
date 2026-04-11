package server

const whatsappHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Claude Bridge — WhatsApp</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
<style>
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
.page-header h1 { font-size: 24px; font-weight: 700; color: var(--text); }

.account-list { display: flex; flex-direction: column; gap: 12px; margin-bottom: 24px; }
.account-card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 20px 24px; display: flex; justify-content: space-between; align-items: center; box-shadow: var(--shadow); }
.account-info { display: flex; align-items: center; gap: 16px; }
.account-avatar { width: 48px; height: 48px; border-radius: 50%; object-fit: cover; background: var(--green-bg); flex-shrink: 0; }
.account-avatar-fallback { width: 48px; height: 48px; border-radius: 50%; background: var(--bg); border: 1px solid var(--border); display: flex; align-items: center; justify-content: center; font-size: 22px; flex-shrink: 0; }
.account-details h4 { font-size: 15px; color: var(--text); margin-bottom: 3px; }
.account-details p { font-size: 13px; color: var(--text-dim); }
.account-actions { display: flex; gap: 8px; align-items: center; }

.empty-state { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 48px 24px; text-align: center; color: var(--text-dim); font-size: 14px; margin-bottom: 24px; box-shadow: var(--shadow); }
.empty-state p { margin-bottom: 16px; }

/* QR overlay/section */
.qr-section { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 24px; margin-bottom: 24px; box-shadow: var(--shadow); }
.qr-section h3 { font-size: 16px; color: var(--text); margin-bottom: 16px; display: flex; align-items: center; justify-content: space-between; }
.qr-container { text-align: center; padding: 16px 0; }
.qr-container img { border-radius: 12px; background: #fff; padding: 16px; }
.qr-placeholder { width: 256px; height: 256px; margin: 0 auto; background: var(--bg); border: 2px dashed var(--border); border-radius: 12px; display: flex; align-items: center; justify-content: center; color: var(--text-dim); font-size: 14px; text-align: center; padding: 20px; }
.qr-instructions { margin-top: 16px; font-size: 14px; color: var(--text-muted); line-height: 1.6; }
.qr-instructions ol { padding-left: 20px; margin-top: 8px; }
.qr-error { color: var(--red); text-align: center; padding: 16px; font-size: 14px; }

.upgrade-banner { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 24px; display: flex; justify-content: space-between; align-items: center; box-shadow: var(--shadow); }
.upgrade-banner .text h4 { color: var(--text); font-size: 16px; margin-bottom: 4px; }
.upgrade-banner .text p { color: var(--text-muted); font-size: 13px; }
.btn-link { background: transparent; color: var(--accent); border: 1px solid var(--accent); padding: 8px 20px; border-radius: 8px; text-decoration: none; font-size: 14px; font-weight: 500; }
.btn-sm { padding: 6px 14px; font-size: 12px; border-radius: 6px; }
.btn-outline-danger { background: transparent; color: var(--red); border: 1px solid var(--red); cursor: pointer; font-weight: 500; }
.btn-outline-danger:hover { background: var(--red); color: #fff; }
</style>
</head>
<body>
<nav class="topnav">
	<div class="logo">Claude <span>Bridge</span></div>
	<a href="/">Dashboard</a>
	<a href="/setup/whatsapp" class="active">WhatsApp</a>
	<a href="/setup/facebook">Facebook</a>
	<div class="spacer"></div>
	<button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle light/dark theme"></button>
</nav>

<div class="container narrow">
	<div class="page-header">
		<div>
			<h1>WhatsApp Accounts</h1>
			<p class="page-subtitle">Manage your connected WhatsApp accounts.</p>
		</div>
		<button class="btn btn-primary" id="addBtn" onclick="addAccount()">+ Add Account</button>
	</div>

	<div id="accountList" class="account-list"></div>

	<div id="qrSection" class="qr-section" style="display:none;">
		<h3>
			<span>Link New Account</span>
			<button class="btn btn-outline btn-sm" onclick="cancelQR()">Cancel</button>
		</h3>
		<div class="qr-container">
			<div id="qrArea">
				<div class="qr-placeholder">Generating QR code...</div>
			</div>
			<div class="qr-instructions">
				<ol>
					<li>Open <strong>WhatsApp</strong> on your phone</li>
					<li>Tap <strong>Settings → Linked Devices → Link a Device</strong></li>
					<li>Point your phone camera at the QR code above</li>
				</ol>
			</div>
		</div>
	</div>

	<div id="emptyState" class="empty-state" style="display:none;">
		<p>No WhatsApp accounts connected yet.</p>
		<button class="btn btn-primary" onclick="addAccount()">+ Connect WhatsApp</button>
	</div>

	<div class="upgrade-banner">
		<div class="text">
			<h4>Need WhatsApp Business API?</h4>
			<p>Get official API access with higher limits and no risk of bans.</p>
		</div>
		<a href="/setup/whatsapp-business" class="btn-link">View Guide</a>
	</div>
</div>

<script>
let polling = null;
let qrPolling = null;

function refresh() {
	fetch('/api/whatsapp/accounts')
		.then(r => r.json())
		.then(data => {
			renderAccounts(data.accounts || []);
			// If a QR flow is active server-side but we're not showing it, show it.
			if (data.qr_active && document.getElementById('qrSection').style.display === 'none') {
				showQRSection();
			}
			if (data.qr_active) updateQR(data);
		})
		.catch(() => {});
}

function renderAccounts(accounts) {
	const list = document.getElementById('accountList');
	const empty = document.getElementById('emptyState');

	if (accounts.length === 0) {
		list.innerHTML = '';
		if (document.getElementById('qrSection').style.display === 'none') {
			empty.style.display = 'block';
		} else {
			empty.style.display = 'none';
		}
		return;
	}
	empty.style.display = 'none';

	let html = '';
	for (const acct of accounts) {
		const connected = acct.status === 'connected';
		const statusClass = connected ? 'connected' : 'disconnected';
		const statusText = connected ? 'Connected' : 'Disconnected';

		const displayName = acct.push_name || acct.phone_number || acct.jid;
		const phone = acct.phone_number ? '+' + acct.phone_number : '';

		let avatarHTML;
		if (acct.profile_pic) {
			avatarHTML = '<img class="account-avatar" src="' + acct.profile_pic + '" alt="">';
		} else {
			avatarHTML = '<div class="account-avatar-fallback">📱</div>';
		}

		let actionsHTML;
		if (connected) {
			actionsHTML = '<button class="btn btn-outline btn-sm" onclick="disconnectAccount(\'' + acct.jid + '\')">Disconnect</button>';
		} else {
			actionsHTML = '<button class="btn btn-primary btn-sm" onclick="reconnectAccount(\'' + acct.jid + '\')">Reconnect</button>' +
				'<button class="btn btn-outline-danger btn-sm" onclick="removeAccount(\'' + acct.jid + '\')">Remove</button>';
		}

		html += '<div class="account-card">' +
			'<div class="account-info">' +
				avatarHTML +
				'<div class="account-details">' +
					'<h4>' + escapeHTML(displayName) + '</h4>' +
					'<p>' + escapeHTML(phone) + ' <span class="status-badge ' + statusClass + '" style="margin-left:8px;">' + statusText + '</span></p>' +
				'</div>' +
			'</div>' +
			'<div class="account-actions">' + actionsHTML + '</div>' +
		'</div>';
	}
	list.innerHTML = html;
}

function addAccount() {
	document.getElementById('emptyState').style.display = 'none';
	showQRSection();

	fetch('/api/whatsapp/accounts/qr', { method: 'POST' })
		.then(r => r.json())
		.then(data => {
			if (data.ok === false) {
				document.getElementById('qrArea').innerHTML =
					'<div class="qr-placeholder" style="color:var(--red);">' + escapeHTML(data.error || 'Failed to start QR flow') + '</div>';
				return;
			}
			startQRPolling();
		})
		.catch(err => {
			document.getElementById('qrArea').innerHTML =
				'<div class="qr-placeholder" style="color:var(--red);">Error: ' + escapeHTML(err.message) + '</div>';
		});
}

function showQRSection() {
	document.getElementById('qrSection').style.display = 'block';
	document.getElementById('addBtn').disabled = true;
	document.getElementById('qrArea').innerHTML = '<div class="qr-placeholder">Generating QR code...</div>';
}

function cancelQR() {
	document.getElementById('qrSection').style.display = 'none';
	document.getElementById('addBtn').disabled = false;
	if (qrPolling) { clearInterval(qrPolling); qrPolling = null; }
	refresh();
}

function startQRPolling() {
	if (qrPolling) clearInterval(qrPolling);
	qrPolling = setInterval(() => {
		fetch('/api/whatsapp/qr')
			.then(r => r.json())
			.then(data => {
				updateQR(data);
			});
	}, 1500);
}

function updateQR(data) {
	if (data.qr_error) {
		document.getElementById('qrArea').innerHTML =
			'<div class="qr-placeholder" style="color:var(--red);">' + escapeHTML(data.qr_error) + '<br><br><button class="btn btn-primary btn-sm" onclick="addAccount()">Try Again</button></div>';
		if (qrPolling) { clearInterval(qrPolling); qrPolling = null; }
		document.getElementById('addBtn').disabled = false;
		return;
	}
	if (!data.qr_active) {
		// QR flow ended (success or timeout). Refresh the account list.
		if (qrPolling) { clearInterval(qrPolling); qrPolling = null; }
		document.getElementById('qrSection').style.display = 'none';
		document.getElementById('addBtn').disabled = false;
		refresh();
		return;
	}
	if (data.qr_code) {
		document.getElementById('qrArea').innerHTML =
			'<img src="' + data.qr_code + '" width="256" height="256" alt="QR Code">';
	}
}

function reconnectAccount(jid) {
	const btn = event.target;
	btn.disabled = true;
	btn.textContent = 'Reconnecting...';
	fetch('/api/whatsapp/accounts/reconnect?jid=' + encodeURIComponent(jid), { method: 'POST' })
		.then(r => r.json())
		.then(data => {
			if (data.ok === false) {
				alert('Reconnect failed: ' + (data.error || 'unknown'));
			}
			setTimeout(refresh, 1000);
		})
		.catch(err => {
			alert('Error: ' + err.message);
			refresh();
		});
}

function disconnectAccount(jid) {
	if (!confirm('Disconnect this account?')) return;
	fetch('/api/whatsapp/accounts/disconnect?jid=' + encodeURIComponent(jid), { method: 'POST' })
		.then(() => setTimeout(refresh, 500))
		.catch(() => refresh());
}

function removeAccount(jid) {
	if (!confirm('Remove this account? You will need to scan the QR code again to reconnect.')) return;
	fetch('/api/whatsapp/accounts/remove?jid=' + encodeURIComponent(jid), { method: 'POST' })
		.then(() => setTimeout(refresh, 500))
		.catch(() => refresh());
}

function escapeHTML(str) {
	if (!str) return '';
	return str.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

// Initial load + auto-refresh.
refresh();
polling = setInterval(refresh, 5000);
</script>
</body>
</html>`
