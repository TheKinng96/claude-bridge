package server

func contactsPage() string {
	return headHTML("Claude Bridge — Contacts") + `<body>` + navHTML("/contacts") + `
<div class="container">
<div class="page-title" style="display:flex;justify-content:space-between;align-items:center;margin-bottom:24px">
  <span>Contacts</span>
  <div style="display:flex;gap:8px">
    <button id="btnGroups" onclick="showView('groups')" style="padding:7px 16px;border-radius:8px;border:1px solid var(--border);background:var(--accent);color:#fff;cursor:pointer;font-size:13px;font-weight:600">Groups</button>
    <button id="btnContacts" onclick="showView('contacts')" style="padding:7px 16px;border-radius:8px;border:1px solid var(--border);background:transparent;color:var(--text-muted);cursor:pointer;font-size:13px;font-weight:600">Contacts</button>
  </div>
</div>

<!-- Groups view -->
<div id="viewGroups">
  <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:14px">
    <div style="display:flex;align-items:center;gap:10px">
      <label style="font-size:13px;color:var(--text-muted)">Global reply mode:</label>
      <select id="globalMode" onchange="saveGlobalMode(this.value)" style="background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:4px 8px;font-size:13px">
        <option value="auto">Auto</option>
        <option value="review">Review</option>
        <option value="off">Off</option>
      </select>
    </div>
    <button onclick="showNewGroupForm()" style="padding:8px 16px;border-radius:8px;border:none;background:var(--accent);color:#fff;cursor:pointer;font-size:13px;font-weight:600">+ New Group</button>
  </div>
  <div id="newGroupForm" style="display:none;background:var(--bg-card);border:1px solid var(--border);border-radius:10px;padding:16px;margin-bottom:12px">
    <div style="display:flex;gap:10px;align-items:flex-end;flex-wrap:wrap">
      <div style="flex:1;min-width:140px">
        <label style="font-size:12px;color:var(--text-muted);display:block;margin-bottom:4px">Name</label>
        <input id="newGroupName" placeholder="Group name" style="width:100%;background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:8px;font-size:13px;box-sizing:border-box">
      </div>
      <div>
        <label style="font-size:12px;color:var(--text-muted);display:block;margin-bottom:4px">Type</label>
        <select id="newGroupType" style="background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:8px;font-size:13px">
          <option value="manual">Manual</option><option value="auto">Auto</option>
        </select>
      </div>
      <div>
        <label style="font-size:12px;color:var(--text-muted);display:block;margin-bottom:4px">Reply Mode</label>
        <select id="newGroupMode" style="background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:8px;font-size:13px">
          <option value="auto">Auto</option><option value="review">Review</option><option value="off">Off</option>
        </select>
      </div>
      <button onclick="createGroup()" style="padding:9px 16px;border-radius:8px;border:none;background:var(--accent);color:#fff;cursor:pointer;font-size:13px;font-weight:600">Create</button>
      <button onclick="document.getElementById('newGroupForm').style.display='none'" style="padding:9px 12px;border-radius:8px;border:1px solid var(--border);background:transparent;color:var(--text-muted);cursor:pointer;font-size:13px">Cancel</button>
    </div>
  </div>
  <table style="width:100%;border-collapse:collapse;font-size:14px">
    <thead><tr style="border-bottom:1px solid var(--border)">
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Name</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Type</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Contacts</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Reply Mode</th>
      <th style="padding:10px 8px"></th>
    </tr></thead>
    <tbody id="groupsTable"></tbody>
  </table>
</div>

<!-- Contacts view -->
<div id="viewContacts" style="display:none">
  <input id="contactSearch" placeholder="Search by name or phone..." oninput="renderContacts()"
    style="width:100%;background:var(--bg-card);color:var(--text);border:1px solid var(--border);border-radius:8px;padding:10px 12px;font-size:14px;margin-bottom:14px;box-sizing:border-box">
  <table style="width:100%;border-collapse:collapse;font-size:14px">
    <thead><tr style="border-bottom:1px solid var(--border)">
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Name</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Phone</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">Groups</th>
      <th style="text-align:left;padding:10px 8px;color:var(--text-muted);font-weight:500">First seen</th>
    </tr></thead>
    <tbody id="contactsTable"></tbody>
  </table>
</div>

<!-- Contact detail slide-in panel -->
<div id="contactPanel" style="display:none;position:fixed;top:0;right:0;width:340px;height:100vh;background:var(--bg-card);border-left:1px solid var(--border);padding:24px;overflow-y:auto;z-index:100">
  <button onclick="closePanel()" style="float:right;background:transparent;border:none;color:var(--text-muted);font-size:18px;cursor:pointer">&#x2715;</button>
  <h3 id="panelName" style="margin:0 0 4px;color:var(--text)"></h3>
  <div id="panelPhone" style="font-size:12px;color:var(--text-dim);margin-bottom:16px"></div>
  <div style="font-size:13px;color:var(--text-muted);margin-bottom:8px;font-weight:500">Manual Groups</div>
  <div id="panelGroups"></div>
</div>
</div>

<script>
let groups = [];
let contacts = [];
let allGroups = [];
let currentContact = null;

async function init() {
  await loadGlobalMode();
  await Promise.all([loadGroups(), loadContacts()]);
}

async function loadGlobalMode() {
  const r = await fetch('/api/agent/config');
  const j = await r.json();
  const sel = document.getElementById('globalMode');
  if (sel && j.global_reply_mode) sel.value = j.global_reply_mode;
}

async function saveGlobalMode(mode) {
  const r = await fetch('/api/agent/config');
  const j = await r.json();
  await fetch('/api/agent/config', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({...j, global_reply_mode: mode})
  });
}

async function loadGroups() {
  const r = await fetch('/api/groups');
  const j = await r.json();
  groups = j.groups || [];
  allGroups = groups;
  renderGroups();
}

async function loadContacts() {
  const r = await fetch('/api/contacts');
  const j = await r.json();
  contacts = j.contacts || [];
  renderContacts();
}

function renderGroups() {
  const tbody = document.getElementById('groupsTable');
  if (groups.length === 0) {
    tbody.innerHTML = '<tr><td colspan="5" style="text-align:center;padding:40px;color:var(--text-muted)">No groups yet. Create one above.</td></tr>';
    return;
  }
  tbody.innerHTML = groups.map(g => '<tr style="border-bottom:1px solid var(--border)">' +
    '<td style="padding:10px 8px;color:var(--text);font-weight:500">' + esc(g.name) + '</td>' +
    '<td style="padding:10px 8px"><span style="font-size:11px;background:var(--bg);border:1px solid var(--border);border-radius:4px;padding:2px 6px;color:var(--text-muted)">' + g.type + '</span></td>' +
    '<td style="padding:10px 8px;color:var(--text-muted)">' + g.contact_count + '</td>' +
    '<td style="padding:10px 8px"><select onchange="updateGroupMode(' + g.id + ', this.value)" style="background:var(--bg);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:4px 8px;font-size:12px">' +
      '<option value="auto"' + (g.reply_mode==='auto'?' selected':'') + '>Auto</option>' +
      '<option value="review"' + (g.reply_mode==='review'?' selected':'') + '>Review</option>' +
      '<option value="off"' + (g.reply_mode==='off'?' selected':'') + '>Off</option>' +
    '</select></td>' +
    '<td style="padding:10px 8px;text-align:right"><button onclick="deleteGroup(' + g.id + ')" style="background:transparent;border:none;color:#ef4444;cursor:pointer;font-size:12px">Delete</button></td>' +
    '</tr>').join('');
}

function renderContacts() {
  const q = (document.getElementById('contactSearch')?.value || '').toLowerCase();
  const filtered = contacts.filter(c =>
    (c.push_name||'').toLowerCase().includes(q) || c.jid.toLowerCase().includes(q));
  const tbody = document.getElementById('contactsTable');
  if (filtered.length === 0) {
    tbody.innerHTML = '<tr><td colspan="4" style="text-align:center;padding:40px;color:var(--text-muted)">No contacts yet.</td></tr>';
    return;
  }
  tbody.innerHTML = filtered.map(c => {
    const phone = c.jid.split('@')[0];
    const badges = (c.groups || []).map(g =>
      '<span style="font-size:11px;background:var(--bg);border:1px solid var(--border);border-radius:4px;padding:2px 6px;color:var(--text-muted);margin-right:4px">' + esc(g.name) + '</span>'
    ).join('');
    return '<tr style="border-bottom:1px solid var(--border);cursor:pointer" onclick="openContact(\'' + escAttr(c.jid) + '\')">' +
      '<td style="padding:10px 8px;color:var(--text);font-weight:500">' + (esc(c.push_name)||'<span style=\"color:var(--text-dim)\">(unknown)</span>') + '</td>' +
      '<td style="padding:10px 8px;color:var(--text-muted);font-family:monospace;font-size:13px">' + phone + '</td>' +
      '<td style="padding:10px 8px">' + badges + '</td>' +
      '<td style="padding:10px 8px;color:var(--text-dim);font-size:12px">' + c.first_seen_at + '</td>' +
      '</tr>';
  }).join('');
}

function showView(view) {
  document.getElementById('viewGroups').style.display = view === 'groups' ? '' : 'none';
  document.getElementById('viewContacts').style.display = view === 'contacts' ? '' : 'none';
  document.getElementById('btnGroups').style.background = view === 'groups' ? 'var(--accent)' : 'transparent';
  document.getElementById('btnGroups').style.color = view === 'groups' ? '#fff' : 'var(--text-muted)';
  document.getElementById('btnContacts').style.background = view === 'contacts' ? 'var(--accent)' : 'transparent';
  document.getElementById('btnContacts').style.color = view === 'contacts' ? '#fff' : 'var(--text-muted)';
}

function showNewGroupForm() {
  document.getElementById('newGroupForm').style.display = '';
}

async function createGroup() {
  const name = document.getElementById('newGroupName').value.trim();
  const type = document.getElementById('newGroupType').value;
  const reply_mode = document.getElementById('newGroupMode').value;
  if (!name) return;
  await fetch('/api/groups', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name, type, reply_mode})
  });
  document.getElementById('newGroupForm').style.display = 'none';
  document.getElementById('newGroupName').value = '';
  loadGroups();
}

async function updateGroupMode(id, mode) {
  const g = groups.find(x => x.id === id);
  if (!g) return;
  await fetch('/api/groups/' + id, {
    method: 'PUT',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({name: g.name, reply_mode: mode})
  });
  loadGroups();
}

async function deleteGroup(id) {
  if (!confirm('Delete this group?')) return;
  await fetch('/api/groups/' + id, {method: 'DELETE'});
  loadGroups();
}

function openContact(jid) {
  const c = contacts.find(x => x.jid === jid);
  if (!c) return;
  currentContact = c;
  document.getElementById('panelName').textContent = c.push_name || '(unknown)';
  document.getElementById('panelPhone').textContent = c.jid.split('@')[0];
  const manualGroups = allGroups.filter(g => g.type === 'manual');
  const assigned = new Set((c.groups || []).map(g => g.id));
  document.getElementById('panelGroups').innerHTML = manualGroups.length === 0
    ? '<div style="color:var(--text-muted);font-size:13px">No manual groups yet.</div>'
    : manualGroups.map(g =>
      '<label style="display:flex;align-items:center;gap:8px;padding:8px 0;cursor:pointer;font-size:14px;color:var(--text);border-bottom:1px solid var(--border)">' +
      '<input type="checkbox"' + (assigned.has(g.id) ? ' checked' : '') + ' onchange="toggleContactGroup(\'' + escAttr(c.jid) + '\',' + g.id + ',this.checked)">' +
      esc(g.name) +
      '<span style="margin-left:auto;font-size:11px;color:var(--text-muted)">' + g.reply_mode + '</span>' +
      '</label>').join('');
  document.getElementById('contactPanel').style.display = '';
}

function closePanel() {
  document.getElementById('contactPanel').style.display = 'none';
  currentContact = null;
}

async function toggleContactGroup(jid, groupID, add) {
  if (add) {
    await fetch('/api/contacts/' + encodeURIComponent(jid) + '/groups', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({group_id: groupID})
    });
  } else {
    await fetch('/api/contacts/' + encodeURIComponent(jid) + '/groups/' + groupID, {method: 'DELETE'});
  }
  await loadContacts();
  if (currentContact) openContact(jid);
}

function esc(s) {
  return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function escAttr(s) {
  return (s||'').replace(/'/g,"\\'").replace(/"/g,'&quot;');
}

init();
</script>
</body></html>`
}
