package server

func messagesPage() string {
	return headHTML("Claude Bridge — Messages") + `<body>` + navHTML("/messages") + `
<div class="container">
<div class="page-title">Messages</div>
<div style="display:flex;gap:2px;margin-bottom:20px;border-bottom:1px solid var(--border)">
  <div class="msg-tab active" id="tabPending" onclick="switchTab('pending')" style="padding:10px 20px;cursor:pointer;font-size:14px;color:var(--accent);border-bottom:2px solid var(--accent);margin-bottom:-1px">Pending</div>
  <div class="msg-tab" id="tabSent" onclick="switchTab('sent')" style="padding:10px 20px;cursor:pointer;font-size:14px;color:var(--text-muted);border-bottom:2px solid transparent;margin-bottom:-1px">Sent</div>
</div>
<div id="bulkBar" style="display:flex;gap:10px;align-items:center;margin-bottom:14px">
  <input type="checkbox" id="selectAll" onchange="toggleSelectAll(this.checked)" style="width:16px;height:16px;cursor:pointer">
  <label for="selectAll" style="font-size:13px;color:var(--text)">Select all</label>
  <button onclick="bulkAction('approve')" style="padding:7px 16px;border-radius:8px;border:none;cursor:pointer;font-size:13px;font-weight:600;background:var(--accent);color:#fff">Approve Selected</button>
  <button onclick="bulkAction('reject')" style="padding:7px 16px;border-radius:8px;border:none;cursor:pointer;font-size:13px;font-weight:600;background:transparent;border:1px solid #ef4444;color:#ef4444">Reject Selected</button>
</div>
<div id="replyList"></div>
</div>
<script>
let currentTab = 'pending';
let replies = [];

async function load() {
  const status = currentTab === 'pending' ? 'pending' : '';
  const r = await fetch('/api/pending-replies?status=' + status);
  const j = await r.json();
  replies = (j.replies || []).filter(x => currentTab === 'pending' ? x.status === 'pending' : x.status !== 'pending');
  render();
}

function switchTab(tab) {
  currentTab = tab;
  ['pending','sent'].forEach(t => {
    const el = document.getElementById('tab' + t.charAt(0).toUpperCase() + t.slice(1));
    if (el) {
      el.style.color = t === tab ? 'var(--accent)' : 'var(--text-muted)';
      el.style.borderBottomColor = t === tab ? 'var(--accent)' : 'transparent';
    }
  });
  document.getElementById('bulkBar').style.display = tab === 'pending' ? 'flex' : 'none';
  document.getElementById('selectAll').checked = false;
  load();
}

function render() {
  const list = document.getElementById('replyList');
  if (replies.length === 0) {
    list.innerHTML = '<div style="text-align:center;color:var(--text-muted);padding:40px;font-size:14px">No ' + currentTab + ' replies.</div>';
    return;
  }
  const editing = currentTab === 'pending';
  list.innerHTML = replies.map(p => {
    const name = p.push_name || p.contact_jid.split('@')[0];
    return '<div class="card" id="card-' + p.id + '">' +
      '<div style="display:flex;align-items:center;gap:10px">' +
      (editing ? '<input type="checkbox" class="row-check" data-id="' + p.id + '" style="width:16px;height:16px;cursor:pointer">' : '') +
      '<span style="font-weight:600;font-size:15px;color:var(--text)">' + esc(name) + '</span>' +
      '<span style="font-size:12px;color:var(--text-dim);margin-left:8px">' + p.created_at + '</span>' +
      '<span style="margin-left:auto;font-size:12px;background:var(--bg);border:1px solid var(--border);border-radius:6px;padding:2px 8px;color:var(--text-muted)">' + p.status + '</span>' +
      '</div>' +
      '<div style="font-size:12px;color:var(--text-muted);margin:8px 0 4px;text-transform:uppercase;letter-spacing:.5px">Incoming</div>' +
      '<div style="font-size:14px;color:var(--text);background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:10px 12px;line-height:1.5;white-space:pre-wrap">' + esc(p.incoming_msg) + '</div>' +
      '<div style="font-size:12px;color:var(--text-muted);margin:8px 0 4px;text-transform:uppercase;letter-spacing:.5px">Proposed reply</div>' +
      '<div style="font-size:14px;color:var(--text);background:var(--bg);border:1px solid var(--border);border-radius:8px;padding:10px 12px;line-height:1.5;white-space:pre-wrap' + (editing ? ';outline:none' : '') + '" id="reply-' + p.id + '"' + (editing ? ' contenteditable="true"' : '') + '>' + esc(p.proposed_reply) + '</div>' +
      (editing ? '<div style="display:flex;gap:8px;margin-top:12px;align-items:center">' +
        '<button onclick="approve(' + p.id + ')" style="padding:7px 16px;border-radius:8px;border:none;cursor:pointer;font-size:13px;font-weight:600;background:var(--accent);color:#fff">Approve</button>' +
        '<button onclick="editSend(' + p.id + ')" style="padding:7px 16px;border-radius:8px;border:none;cursor:pointer;font-size:13px;font-weight:600;background:#10b981;color:#fff">Edit &amp; Send</button>' +
        '<button onclick="reject(' + p.id + ')" style="padding:7px 16px;border-radius:8px;cursor:pointer;font-size:13px;font-weight:600;background:transparent;border:1px solid #ef4444;color:#ef4444">Reject</button>' +
        '</div>' : '') +
      '</div>';
  }).join('');
}

function esc(s) {
  return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

async function approve(id) {
  await fetch('/api/pending-replies/' + id + '/approve', {method:'POST'});
  load();
}

async function reject(id) {
  await fetch('/api/pending-replies/' + id + '/reject', {method:'POST'});
  load();
}

async function editSend(id) {
  const el = document.getElementById('reply-' + id);
  const reply = el ? el.innerText.trim() : '';
  if (!reply) return;
  await fetch('/api/pending-replies/' + id + '/edit-send', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({reply})
  });
  load();
}

function toggleSelectAll(checked) {
  document.querySelectorAll('.row-check').forEach(c => c.checked = checked);
}

async function bulkAction(action) {
  const checked = [...document.querySelectorAll('.row-check:checked')].map(c => parseInt(c.dataset.id));
  if (checked.length === 0) return;
  await Promise.all(checked.map(id => fetch('/api/pending-replies/' + id + '/' + action, {method:'POST'})));
  document.getElementById('selectAll').checked = false;
  load();
}

load();
setInterval(load, 30000);
</script>
</body></html>`
}
