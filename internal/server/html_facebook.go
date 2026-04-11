package server

const facebookHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>CRM Agent — Facebook Setup</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
</head>
<body>
<nav class="topnav">
	<div class="logo">CRM <span>Agent</span></div>
	<a href="/">Dashboard</a>
	<a href="/setup/whatsapp">WhatsApp</a>
	<a href="/setup/facebook" class="active">Facebook</a>
	<div class="spacer"></div>
	<button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle light/dark theme"></button>
</nav>

<div class="container narrow">
	<h1 class="page-title">Facebook Messenger</h1>
	<p class="page-subtitle">Connect your Facebook Page to receive and respond to Messenger conversations.</p>

	<div class="notice" style="margin-bottom:24px;">
		Facebook Messenger integration is coming in a future update. The form below is a preview of the setup flow.
	</div>

	<div class="card">
		<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:24px;">
			<h3>Page Connection</h3>
			<span class="status-badge disconnected" id="fbBadge">Not Connected</span>
		</div>
		<div class="form-group">
			<label>Page ID</label>
			<input type="text" id="pageId" placeholder="e.g. 123456789012345">
			<div class="hint">Found in your Facebook Page → About → Page ID</div>
		</div>
		<div class="form-group">
			<label>Page Access Token</label>
			<input type="text" id="pageToken" placeholder="EAAxxxxxxx...">
			<div class="hint">Generate from Meta Developer Console → Your App → Messenger → Settings</div>
		</div>
		<button class="btn btn-primary" onclick="connectFB()">Connect Page</button>
	</div>
</div>

<script>
function connectFB() {
	const pageId = document.getElementById('pageId').value.trim();
	const token = document.getElementById('pageToken').value.trim();
	if (!pageId || !token) { alert('Please enter both Page ID and Access Token.'); return; }

	fetch('/api/facebook/connect', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ page_id: pageId, token: token })
	})
	.then(r => r.json())
	.then(data => {
		if (data.ok) {
			document.getElementById('fbBadge').className = 'status-badge connected';
			document.getElementById('fbBadge').textContent = 'Connected';
		} else {
			alert('Connection failed: ' + (data.error || 'unknown error'));
		}
	})
	.catch(err => alert('Error: ' + err.message));
}

fetch('/api/status')
	.then(r => r.json())
	.then(data => {
		if (data.facebook && data.facebook.connected) {
			document.getElementById('fbBadge').className = 'status-badge connected';
			document.getElementById('fbBadge').textContent = 'Connected';
		}
	});
</script>
</body>
</html>`
