package server

const agentHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Claude Bridge — Agent</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
<style>
.page-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 24px; }
.page-header h1 { font-size: 24px; font-weight: 700; color: var(--text); }

.card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 20px 24px; box-shadow: var(--shadow); margin-bottom: 20px; }
.card h3 { font-size: 16px; color: var(--text); margin-bottom: 12px; }
.card .help { font-size: 12px; color: var(--text-dim); margin-bottom: 12px; }

.form-row { display: flex; flex-direction: column; gap: 6px; margin-bottom: 14px; }
.form-row label { font-size: 13px; color: var(--text-muted); }
.form-row input, .form-row select, .form-row textarea {
  background: var(--bg); color: var(--text);
  border: 1px solid var(--border); border-radius: 8px;
  padding: 10px 12px; font-size: 14px; font-family: inherit;
}
.form-row textarea { resize: vertical; min-height: 100px; }
.form-row input:focus, .form-row select:focus, .form-row textarea:focus { outline: none; border-color: var(--accent); }

.toggle-row { display: flex; align-items: center; gap: 12px; margin-bottom: 14px; }
.toggle-row label { font-size: 14px; color: var(--text); font-weight: 500; }
.toggle { position: relative; width: 44px; height: 24px; }
.toggle input { opacity: 0; width: 0; height: 0; }
.toggle-slider { position: absolute; cursor: pointer; top: 0; left: 0; right: 0; bottom: 0; background: var(--border); border-radius: 24px; transition: 0.2s; }
.toggle-slider:before { position: absolute; content: ""; height: 18px; width: 18px; left: 3px; bottom: 3px; background: white; border-radius: 50%; transition: 0.2s; }
.toggle input:checked + .toggle-slider { background: var(--accent); }
.toggle input:checked + .toggle-slider:before { transform: translateX(20px); }

.actions-row { display: flex; gap: 10px; margin-top: 4px; }
.btn { padding: 9px 18px; border-radius: 8px; font-size: 14px; font-weight: 500; cursor: pointer; border: none; transition: background 0.15s; }
.btn-primary { background: var(--accent); color: #fff; }
.btn-primary:hover { background: var(--accent-hover); }
.btn-outline { background: transparent; color: var(--text-muted); border: 1px solid var(--border); }
.btn-outline:hover { border-color: var(--border-hover); color: var(--text); }
.btn-sm { padding: 5px 12px; font-size: 13px; }
.btn-danger { background: var(--red-bg); color: var(--red); border: 1px solid var(--red-border); }

.flow-step { display: flex; gap: 8px; align-items: flex-start; margin-bottom: 10px; background: var(--bg); border: 1px solid var(--border); border-radius: 8px; padding: 10px 12px; }
.flow-step-num { min-width: 24px; height: 24px; background: var(--accent); color: #fff; border-radius: 50%; display: flex; align-items: center; justify-content: center; font-size: 12px; font-weight: 700; margin-top: 2px; }
.flow-step-body { flex: 1; display: flex; flex-direction: column; gap: 6px; }
.flow-step-body input { font-size: 13px; font-weight: 600; }
.flow-step-body textarea { font-size: 13px; min-height: 56px; }

.stats-row { display: flex; gap: 12px; margin-bottom: 20px; flex-wrap: wrap; }
.stat { flex: 1; min-width: 100px; background: var(--bg-card); border: 1px solid var(--border); border-radius: 10px; padding: 14px 18px; text-align: center; box-shadow: var(--shadow); }
.stat .num { font-size: 22px; font-weight: 700; color: var(--text); }
.stat .lbl { font-size: 12px; color: var(--text-dim); margin-top: 2px; }

.reply-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.reply-table th { text-align: left; padding: 8px 10px; color: var(--text-dim); border-bottom: 1px solid var(--border); font-weight: 500; }
.reply-table td { padding: 8px 10px; border-bottom: 1px solid var(--border); color: var(--text); vertical-align: top; }
.reply-table td.dim { color: var(--text-dim); }
.reply-table tr:last-child td { border-bottom: none; }
.empty-row td { color: var(--text-dim); text-align: center; padding: 24px; }

.badge { display: inline-block; padding: 2px 8px; border-radius: 4px; font-size: 11px; font-weight: 600; }
.badge-green { background: var(--green-bg); color: var(--green); border: 1px solid var(--green-border); }
</style>
</head>
<body>
<nav class="topnav">
	<a href="/">Dashboard</a>
	<a href="/setup/whatsapp">WhatsApp</a>
	<a href="/setup/facebook">Facebook</a>
	<a href="/setup/knowledge">Knowledge</a>
	<a href="/setup/agent" class="active">Agent</a>
</nav>
<div class="container">
	<div class="page-header">
		<div>
			<h1>Auto-Reply Agent</h1>
			<p class="page-subtitle">AI agent that reads incoming WhatsApp messages, searches your knowledge base, and replies following your configured flow.</p>
		</div>
	</div>

	<div id="banners"></div>

	<div class="card">
		<h3>Agent Settings</h3>
		<div class="toggle-row">
			<label class="toggle">
				<input type="checkbox" id="enabled">
				<span class="toggle-slider"></span>
			</label>
			<label for="enabled">Enable auto-reply</label>
		</div>
		<div class="form-row">
			<label for="model">Model</label>
			<select id="model">
				<option value="claude-haiku-4-5">Haiku 4.5 (fast, cheap — recommended)</option>
				<option value="claude-sonnet-4-6">Sonnet 4.6 (smarter)</option>
				<option value="claude-opus-4-7">Opus 4.7 (best, expensive)</option>
			</select>
		</div>
		<div class="form-row">
			<label for="systemPrompt">Agent identity &amp; tone</label>
			<textarea id="systemPrompt" rows="5" placeholder="You are Sarah, a friendly insurance consultant at ABC Insurance. You help clients find the right insurance plan. Always be warm, concise, and professional. Never make promises about pricing without consulting a specialist."></textarea>
		</div>
		<div class="actions-row">
			<button class="btn btn-primary" onclick="saveConfig()">Save</button>
			<button class="btn btn-outline" onclick="loadAll()">Reload</button>
		</div>
	</div>

	<div class="card">
		<h3>Admin Numbers</h3>
		<div class="help">Admins receive pending-reply notifications and can send <code>!login</code> to get a remote dashboard link.</div>
		<div id="adminChips" style="display:flex;flex-wrap:wrap;gap:6px;margin-bottom:12px;min-height:28px"></div>
		<input id="contactSearch" placeholder="Search contacts to add..." oninput="filterContacts(this.value)"
			style="width:100%;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:9px 12px;font-size:13px;margin-bottom:8px;box-sizing:border-box">
		<div id="contactList" style="max-height:220px;overflow-y:auto;border:1px solid var(--border);border-radius:8px;display:none"></div>
	</div>

	<div class="card">
		<h3>Obsidian Sync</h3>
		<div class="help">Optional. When set, the server writes one markdown file per client into <code>&lt;vault&gt;/Clients/</code> with wikilinks to <code>&lt;vault&gt;/Topics/</code>. Open the vault in Obsidian to see the graph view. <strong>One-way</strong> — manual edits outside the "Custom Notes" section are overwritten on next profile update.</div>
		<div class="form-row">
			<label for="obsidianVault">Vault path</label>
			<input id="obsidianVault" placeholder="/Users/me/ObsidianVault" style="width:100%;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:9px 12px;font-size:13px;box-sizing:border-box">
		</div>
	</div>

	<div class="card">
		<h3>Conversation Flow</h3>
		<div class="help">Define stages the agent follows in order. Claude uses these as a guide — not rigid rules — adapting naturally to the conversation.</div>
		<div id="flowSteps"></div>
		<div class="actions-row" style="margin-top:12px;">
			<button class="btn btn-outline btn-sm" onclick="addStep()">+ Add step</button>
		</div>
	</div>

	<div class="stats-row">
		<div class="stat"><div class="num" id="statReplies">0</div><div class="lbl">Replies sent</div></div>
		<div class="stat"><div class="num" id="statConvos">0</div><div class="lbl">Conversations</div></div>
	</div>

	<div class="card">
		<h3>Recent replies</h3>
		<table class="reply-table">
			<thead>
				<tr>
					<th>Time</th>
					<th>Contact</th>
					<th>Reply sent</th>
				</tr>
			</thead>
			<tbody id="replyBody">
				<tr class="empty-row"><td colspan="3">No replies yet.</td></tr>
			</tbody>
		</table>
	</div>
</div>

<script>
let flowSteps = [];
let ownerJIDs = [];
let allContacts = [];

async function loadAll() {
	await loadConfig();
	await loadReplies();
	await loadContacts();
}

async function loadConfig() {
	try {
		const r = await fetch('/api/agent/config');
		const j = await r.json();
		document.getElementById('enabled').checked = !!j.enabled;
		document.getElementById('model').value = j.model || 'claude-haiku-4-5';
		document.getElementById('systemPrompt').value = j.system_prompt || '';
		flowSteps = j.flow_steps || [];
		ownerJIDs = j.owner_jids || [];
		document.getElementById('obsidianVault').value = j.obsidian_vault_path || '';
		renderSteps();
		renderAdminChips();
	} catch(e) { console.error(e); }
}

async function loadContacts() {
	try {
		const r = await fetch('/api/whatsapp/contacts');
		const j = await r.json();
		allContacts = j.contacts || [];
	} catch(e) {}
}

function filterContacts(q) {
	const list = document.getElementById('contactList');
	if (!q.trim()) { list.style.display = 'none'; return; }
	const lower = q.toLowerCase();
	const filtered = allContacts.filter(c =>
		(c.push_name||'').toLowerCase().includes(lower) || c.jid.toLowerCase().includes(lower)
	).slice(0, 20);
	if (!filtered.length) { list.style.display = 'none'; return; }
	list.style.display = '';
	list.innerHTML = filtered.map(c => {
		const phone = c.jid.split('@')[0];
		const already = ownerJIDs.includes(c.jid);
		return '<div style="display:flex;align-items:center;gap:10px;padding:9px 12px;cursor:pointer;border-bottom:1px solid var(--border);' +
			(already ? 'opacity:.5;pointer-events:none' : 'hover:background:var(--bg-hover)') +
			'" onclick="addAdmin(\'' + esc(c.jid) + '\',\'' + esc(c.push_name||phone) + '\')">' +
			'<div style="flex:1"><div style="font-size:13px;font-weight:600;color:var(--text)">' + esc(c.push_name||'(unknown)') + '</div>' +
			'<div style="font-size:12px;color:var(--text-dim)">' + phone + '</div></div>' +
			(already ? '<span style="font-size:11px;color:var(--green)">✓ added</span>' : '') +
			'</div>';
	}).join('');
}

function addAdmin(jid, name) {
	if (!ownerJIDs.includes(jid)) {
		ownerJIDs.push(jid);
		renderAdminChips();
		filterContacts(document.getElementById('contactSearch').value);
	}
}

function removeAdmin(jid) {
	ownerJIDs = ownerJIDs.filter(j => j !== jid);
	renderAdminChips();
	filterContacts(document.getElementById('contactSearch').value);
}

function renderAdminChips() {
	const el = document.getElementById('adminChips');
	if (!ownerJIDs.length) {
		el.innerHTML = '<span style="font-size:13px;color:var(--text-dim)">No admins set.</span>';
		return;
	}
	el.innerHTML = ownerJIDs.map(jid => {
		const c = allContacts.find(x => x.jid === jid);
		const label = (c && c.push_name) ? c.push_name : jid.split('@')[0];
		return '<span style="display:inline-flex;align-items:center;gap:6px;background:var(--accent);color:#fff;border-radius:20px;padding:4px 10px 4px 12px;font-size:12px;font-weight:600">' +
			esc(label) +
			'<button onclick="removeAdmin(\'' + esc(jid) + '\')" style="background:transparent;border:none;color:#fff;cursor:pointer;font-size:14px;line-height:1;padding:0;margin:0">&#x2715;</button>' +
			'</span>';
	}).join('');
}

function esc(s) {
	return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/'/g,"&#39;");
}

async function loadReplies() {
	try {
		const r = await fetch('/api/agent/replies');
		const j = await r.json();
		const tbody = document.getElementById('replyBody');
		const replies = j.replies || [];
		document.getElementById('statReplies').textContent = replies.length;
		const convos = new Set(replies.map(r => r.conversation_id));
		document.getElementById('statConvos').textContent = convos.size;
		if (!replies.length) {
			tbody.innerHTML = '<tr class="empty-row"><td colspan="3">No replies yet.</td></tr>';
			return;
		}
		tbody.innerHTML = replies.slice(0, 30).map(m => {
			const t = new Date(m.timestamp).toLocaleString();
			const contact = m.conversation_id ? m.conversation_id.split('@')[0] : '—';
			return '<tr><td class="dim">' + t + '</td><td>' + escapeHTML(contact) + '</td><td>' + escapeHTML(m.content) + '</td></tr>';
		}).join('');
	} catch(e) { console.error(e); }
}

function renderSteps() {
	const el = document.getElementById('flowSteps');
	if (!flowSteps.length) {
		el.innerHTML = '<p style="color:var(--text-dim);font-size:13px;">No steps yet. Add one below.</p>';
		return;
	}
	el.innerHTML = flowSteps.map((s, i) => '<div class="flow-step">' +
		'<div class="flow-step-num">' + (i+1) + '</div>' +
		'<div class="flow-step-body">' +
			'<input type="text" value="' + escapeHTML(s.name) + '" oninput="flowSteps['+i+'].name=this.value" placeholder="Step name">' +
			'<textarea oninput="flowSteps['+i+'].instruction=this.value" placeholder="What should the agent do in this step?">' + escapeHTML(s.instruction) + '</textarea>' +
		'</div>' +
		'<button class="btn btn-danger btn-sm" onclick="removeStep('+i+')">✕</button>' +
	'</div>').join('');
}

function addStep() {
	flowSteps.push({name: 'Step ' + (flowSteps.length+1), instruction: ''});
	renderSteps();
}

function removeStep(i) {
	flowSteps.splice(i, 1);
	renderSteps();
}

async function saveConfig() {
	// Re-read textarea values since oninput may not fire on all browsers
	document.querySelectorAll('.flow-step').forEach((el, i) => {
		if (flowSteps[i]) {
			flowSteps[i].name = el.querySelector('input').value;
			flowSteps[i].instruction = el.querySelector('textarea').value;
		}
	});
	const body = {
		enabled: document.getElementById('enabled').checked,
		model: document.getElementById('model').value,
		system_prompt: document.getElementById('systemPrompt').value,
		flow_steps: flowSteps,
		owner_jids: ownerJIDs,
		obsidian_vault_path: document.getElementById('obsidianVault').value.trim(),
	};
	const r = await fetch('/api/agent/config', {
		method: 'POST',
		headers: {'Content-Type': 'application/json'},
		body: JSON.stringify(body),
	});
	const j = await r.json();
	if (!j.ok) alert('Save failed: ' + (j.error || 'unknown'));
	else await loadAll();
}

function escapeHTML(s) {
	return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

loadAll();
setInterval(loadReplies, 10000);
</script>
</body>
</html>`
