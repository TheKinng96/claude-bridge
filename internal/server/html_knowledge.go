package server

const knowledgeHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Claude Bridge — Knowledge</title>
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
.form-row input, .form-row select {
  background: var(--bg); color: var(--text);
  border: 1px solid var(--border); border-radius: 8px;
  padding: 10px 12px; font-size: 14px; font-family: inherit;
}
.form-row input:focus, .form-row select:focus { outline: none; border-color: var(--accent); }

.stats-row { display: flex; gap: 12px; margin-bottom: 20px; flex-wrap: wrap; }
.stat { flex: 1; min-width: 100px; background: var(--bg-card); border: 1px solid var(--border); border-radius: 10px; padding: 14px 18px; text-align: center; box-shadow: var(--shadow); }
.stat .num { font-size: 22px; font-weight: 700; color: var(--text); }
.stat .lbl { font-size: 12px; color: var(--text-dim); text-transform: uppercase; letter-spacing: 0.5px; margin-top: 4px; }

.actions-row { display: flex; gap: 10px; margin-bottom: 14px; flex-wrap: wrap; }

.doc-table { width: 100%; border-collapse: collapse; font-size: 13px; }
.doc-table th, .doc-table td { padding: 10px 12px; border-bottom: 1px solid var(--border); text-align: left; color: var(--text); }
.doc-table th { color: var(--text-muted); font-weight: 500; font-size: 12px; text-transform: uppercase; letter-spacing: 0.5px; }
.doc-table td.actions { white-space: nowrap; }
.status-badge { display: inline-block; padding: 2px 8px; border-radius: 6px; font-size: 11px; font-weight: 500; text-transform: uppercase; letter-spacing: 0.5px; }
.status-ready { background: var(--green-bg); color: var(--green); }
.status-failed { background: #ffe4e4; color: #c02020; }
.status-pending, .status-extracting, .status-classifying { background: var(--bg); color: var(--text-muted); border: 1px solid var(--border); }

.banner { padding: 12px 16px; border-radius: 8px; margin-bottom: 16px; font-size: 14px; }
.banner-warn { background: #fff4d4; color: #8a5a00; border: 1px solid #f5d77a; }
.banner-err  { background: #ffe4e4; color: #a02020; border: 1px solid #f0a0a0; }

.btn-sm { padding: 6px 14px; font-size: 12px; border-radius: 6px; }
.btn-danger { background: transparent; color: var(--red); border: 1px solid var(--red); cursor: pointer; }
.btn-danger:hover { background: var(--red); color: #fff; }
.empty-row td { text-align: center; color: var(--text-dim); padding: 28px; font-style: italic; }

.error-txt { color: var(--red); font-size: 12px; }
</style>
</head>
<body>
<nav class="topnav">
	<div class="logo">Claude <span>Bridge</span></div>
	<a href="/">Dashboard</a>
	<a href="/setup/whatsapp">WhatsApp</a>
	<a href="/setup/facebook">Facebook</a>
	<a href="/setup/knowledge" class="active">Knowledge</a>
	<a href="/setup/agent">Agent</a>
	<div class="spacer"></div>
	<button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle light/dark theme"></button>
</nav>

<div class="container narrow">
	<div class="page-header">
		<div>
			<h1>Knowledge Base</h1>
			<p class="page-subtitle">Drop policy documents, brochures, and promo materials into a folder. Claude classifies, indexes, and exposes them to Claude Desktop via MCP tools.</p>
		</div>
	</div>

	<div id="banners"></div>

	<div class="card">
		<h3>Configuration</h3>
		<div class="help">Uses Claude Code for classification — no API key required. Each new or changed file triggers one classification call. Haiku 4.5 is recommended — fast + cheap.</div>
		<div class="form-row">
			<label for="folderPath">Folder path</label>
			<div style="display:flex;gap:8px;align-items:center;">
				<input type="text" id="folderPath" placeholder="/Users/you/Documents/insurance-docs" style="flex:1;">
				<button class="btn btn-outline btn-sm" onclick="browseFolder()">Browse…</button>
			</div>
		</div>
		<div class="form-row">
			<label for="model">Model</label>
			<select id="model">
				<option value="claude-haiku-4-5">Haiku 4.5 (fast, cheap — recommended)</option>
				<option value="claude-sonnet-4-6">Sonnet 4.6</option>
				<option value="claude-opus-4-7">Opus 4.7 (expensive)</option>
			</select>
		</div>
		<div class="form-row">
			<label for="classifyDelay">Delay between classifications (seconds)</label>
			<input type="number" id="classifyDelay" min="1" max="300" placeholder="10" style="width:100px;">
			<span style="color:var(--text-dim);font-size:13px;margin-left:8px;">Default: 10s — prevents token burn on large folders</span>
		</div>
		<div class="form-row">
			<label for="sessionLimit">Max new files per startup</label>
			<input type="number" id="sessionLimit" min="1" max="1000" placeholder="20" style="width:100px;">
			<span style="color:var(--text-dim);font-size:13px;margin-left:8px;">Default: 20 — use Rescan to classify more</span>
		</div>
		<div class="actions-row">
			<button class="btn btn-primary" onclick="saveConfig()">Save</button>
			<button class="btn btn-outline" onclick="loadAll()">Reload</button>
		</div>
	</div>

	<div class="stats-row">
		<div class="stat"><div class="num" id="statTotal">0</div><div class="lbl">Total</div></div>
		<div class="stat"><div class="num" id="statReady">0</div><div class="lbl">Ready</div></div>
		<div class="stat"><div class="num" id="statPending">0</div><div class="lbl">Pending</div></div>
		<div class="stat"><div class="num" id="statFailed">0</div><div class="lbl">Failed</div></div>
		<div class="stat"><div class="num" id="statCalls">0</div><div class="lbl">API calls</div></div>
	</div>

	<div class="card">
		<h3>Documents</h3>
		<div class="actions-row">
			<button class="btn btn-primary btn-sm" onclick="indexAll()">Index All</button>
			<button class="btn btn-outline btn-sm" onclick="retryFailed()">Retry failed</button>
		</div>
		<p class="help" style="margin-bottom:8px;">Files in your folder are <strong>not indexed automatically</strong>. Click <em>Index All</em> to classify new files — each file uses one Claude API call.</p>
		<table class="doc-table">
			<thead>
				<tr>
					<th>Filename</th>
					<th>Type</th>
					<th>Product</th>
					<th>Lang</th>
					<th>Status</th>
					<th>Summary</th>
					<th></th>
				</tr>
			</thead>
			<tbody id="docBody">
				<tr class="empty-row"><td colspan="7">No documents yet. Configure a folder above.</td></tr>
			</tbody>
		</table>
	</div>
</div>

<script>
async function browseFolder() {
	try {
		const r = await fetch('/api/browse-folder');
		const j = await r.json();
		if (j.ok) document.getElementById('folderPath').value = j.path;
	} catch (e) { console.error(e); }
}

async function loadAll() {
	await loadConfig();
	await loadDocs();
}

async function loadConfig() {
	try {
		const r = await fetch('/api/knowledge/config');
		const j = await r.json();
		document.getElementById('folderPath').value = j.folder_path || '';
		document.getElementById('model').value = j.model || 'claude-haiku-4-5';
		document.getElementById('classifyDelay').value = j.classify_delay_sec || 10;
		document.getElementById('sessionLimit').value = j.session_limit || 20;
		document.getElementById('statCalls').textContent = j.api_calls || 0;
		const banners = document.getElementById('banners');
		banners.innerHTML = '';
		if (j.folder_path && !j.watcher_running) {
			banners.innerHTML += '<div class="banner banner-err">Folder is set but the watcher is not running. The path may not exist or be readable.</div>';
		}
	} catch (e) { console.error(e); }
}

async function saveConfig() {
	const body = {
		folder_path: document.getElementById('folderPath').value.trim(),
		model: document.getElementById('model').value,
		classify_delay_sec: parseInt(document.getElementById('classifyDelay').value) || 10,
		session_limit: parseInt(document.getElementById('sessionLimit').value) || 20,
	};
	const r = await fetch('/api/knowledge/config', {
		method: 'POST',
		headers: {'Content-Type': 'application/json'},
		body: JSON.stringify(body),
	});
	const j = await r.json();
	if (j.ok) {
		await loadAll();
	} else {
		alert('Save failed: ' + (j.error || 'unknown'));
	}
}

async function loadDocs() {
	try {
		const r = await fetch('/api/documents?limit=500');
		const j = await r.json();
		const docs = j.documents || [];
		const stats = j.stats || {};
		document.getElementById('statTotal').textContent = stats.total || 0;
		document.getElementById('statReady').textContent = stats.ready || 0;
		document.getElementById('statPending').textContent = (stats.pending || 0) + (stats.extracting || 0) + (stats.classifying || 0);
		document.getElementById('statFailed').textContent = stats.failed || 0;
		document.getElementById('statCalls').textContent = j.api_calls || 0;
		const body = document.getElementById('docBody');
		if (docs.length === 0) {
			body.innerHTML = '<tr class="empty-row"><td colspan="7">No documents yet. Drop files into the configured folder.</td></tr>';
			return;
		}
		body.innerHTML = docs.map(d => {
			const cls = 'status-' + d.status;
			const err = d.error ? '<div class="error-txt">' + escapeHTML(d.error) + '</div>' : '';
			return '<tr>' +
				'<td>' + escapeHTML(d.filename) + '</td>' +
				'<td>' + escapeHTML(d.doc_type || '') + '</td>' +
				'<td>' + escapeHTML(d.product || '') + '</td>' +
				'<td>' + escapeHTML(d.language || '') + '</td>' +
				'<td><span class="status-badge ' + cls + '">' + d.status + '</span>' + err + '</td>' +
				'<td>' + escapeHTML((d.summary || '').slice(0, 120)) + '</td>' +
				'<td class="actions">' +
					'<button class="btn btn-outline btn-sm" onclick="reingest(' + d.id + ')">Re-ingest</button> ' +
					'<button class="btn btn-danger btn-sm" onclick="delDoc(' + d.id + ')">Delete</button>' +
				'</td>' +
			'</tr>';
		}).join('');
	} catch (e) { console.error(e); }
}

async function indexAll() {
	try {
		const r = await fetch('/api/documents/unindexed-count');
		const j = await r.json();
		const n = j.count || 0;
		if (n === 0) {
			alert('All files in the folder are already indexed. Nothing to do.');
			return;
		}
		const ok = confirm(
			'Index ' + n + ' file' + (n === 1 ? '' : 's') + '?\n\n' +
			'Each file uses one Claude API call (token cost depends on file size and model).\n\n' +
			'Classification runs at the configured delay between files to avoid burning through tokens too fast.\n\n' +
			'Continue?'
		);
		if (!ok) return;
		await fetch('/api/documents/rescan', {method: 'POST'});
		setTimeout(loadDocs, 600);
	} catch (e) { console.error(e); }
}

async function retryFailed() {
	const r = await fetch('/api/documents?status=failed&limit=500');
	const j = await r.json();
	for (const d of (j.documents || [])) {
		await fetch('/api/documents/' + d.id + '/reingest', {method: 'POST'});
	}
	setTimeout(loadDocs, 600);
}

async function reingest(id) {
	await fetch('/api/documents/' + id + '/reingest', {method: 'POST'});
	setTimeout(loadDocs, 600);
}

async function delDoc(id) {
	if (!confirm('Delete document row? File on disk is untouched.')) return;
	await fetch('/api/documents/' + id, {method: 'DELETE'});
	await loadDocs();
}

function escapeHTML(s) {
	return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

loadAll();
setInterval(loadDocs, 4000);
</script>
</body>
</html>`
