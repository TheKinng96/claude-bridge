package server

const whatsappBusinessHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>CRM Agent — WhatsApp Business API Guide</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
<style>
.step { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 24px; margin-bottom: 16px; box-shadow: var(--shadow); }
.step-header { display: flex; align-items: center; gap: 12px; margin-bottom: 12px; }
.step-num { width: 28px; height: 28px; border-radius: 50%; background: var(--accent); color: #fff; display: flex; align-items: center; justify-content: center; font-size: 14px; font-weight: 700; flex-shrink: 0; }
.step h3 { font-size: 16px; color: var(--text); }
.step p { font-size: 14px; color: var(--text-muted); line-height: 1.6; }
.step code { background: var(--code-bg); padding: 2px 6px; border-radius: 4px; font-size: 13px; color: var(--code-color); }
.back-link { display: inline-flex; align-items: center; gap: 6px; color: var(--text-muted); text-decoration: none; font-size: 14px; margin-bottom: 24px; }
.back-link:hover { color: var(--text); }
</style>
</head>
<body>
<nav class="topnav">
	<div class="logo">CRM <span>Agent</span></div>
	<a href="/">Dashboard</a>
	<a href="/setup/whatsapp" class="active">WhatsApp</a>
	<a href="/setup/facebook">Facebook</a>
	<div class="spacer"></div>
	<button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle light/dark theme"></button>
</nav>

<div class="container narrow">
	<a href="/setup/whatsapp" class="back-link">← Back to WhatsApp Setup</a>
	<h1 class="page-title">WhatsApp Business API Setup</h1>
	<p class="page-subtitle">Follow these steps to upgrade from personal WhatsApp to the official Business API with higher limits and no risk of bans.</p>

	<div class="step">
		<div class="step-header"><div class="step-num">1</div><h3>Create a Meta Business Account</h3></div>
		<p>Go to <a href="https://business.facebook.com" target="_blank">business.facebook.com</a> and create a business account if you don't have one. You'll need a Facebook account to get started.</p>
	</div>
	<div class="step">
		<div class="step-header"><div class="step-num">2</div><h3>Create a Meta Developer App</h3></div>
		<p>Visit <a href="https://developers.facebook.com" target="_blank">developers.facebook.com</a> → My Apps → Create App. Choose "Business" type and link it to your Meta Business Account.</p>
	</div>
	<div class="step">
		<div class="step-header"><div class="step-num">3</div><h3>Add the WhatsApp Product</h3></div>
		<p>In your app dashboard, click "Add Product" and select WhatsApp. This will generate a test phone number and temporary access token.</p>
	</div>
	<div class="step">
		<div class="step-header"><div class="step-num">4</div><h3>Register Your Phone Number</h3></div>
		<p>Go to WhatsApp → Getting Started in the Meta Developer Console. Add your real business phone number (you'll receive an OTP to verify). Note down the <code>Phone Number ID</code>.</p>
	</div>
	<div class="step">
		<div class="step-header"><div class="step-num">5</div><h3>Generate a Permanent Access Token</h3></div>
		<p>Go to Business Settings → System Users → create one → assign the WhatsApp app with <code>whatsapp_business_messaging</code> permission → generate a token. This token does not expire.</p>
	</div>
	<div class="step">
		<div class="step-header"><div class="step-num">6</div><h3>Configure Webhook</h3></div>
		<p>In the Meta Developer Console under WhatsApp → Configuration, set the webhook URL to your server's public address: <code>https://your-domain.com/api/whatsapp/webhook</code>. Subscribe to <code>messages</code> events. The verify token should match your app config.</p>
	</div>
	<div class="step">
		<div class="step-header"><div class="step-num">7</div><h3>Enter Credentials in CRM Agent</h3></div>
		<p>Once Phase 2 of CRM Agent ships with Business API support, you'll enter your Phone Number ID and permanent access token in the settings page. The webhook will be handled automatically.</p>
	</div>
</div>
</body>
</html>`
