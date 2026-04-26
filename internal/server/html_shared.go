package server

// sharedCSS is the CSS variables + theme toggle styles, served at /static/theme.css.
// All pages link to this. Theme preference is stored in localStorage.
const sharedCSS = `
/* ===== Theme variables ===== */
:root {
  --bg: #0f1117;
  --bg-card: #1a1d2e;
  --bg-input: #0f1117;
  --bg-hover: #141726;
  --border: #2d3154;
  --border-hover: #3d4574;
  --text: #e2e8f0;
  --text-muted: #94a3b8;
  --text-dim: #64748b;
  --accent: #3b4fd8;
  --accent-hover: #4f63e8;
  --green: #22c55e;
  --red: #ef4444;
  --yellow: #eab308;
  --green-bg: #22c55e20;
  --red-bg: #ef444420;
  --yellow-bg: #eab30820;
  --green-border: #22c55e40;
  --red-border: #ef444440;
  --yellow-border: #eab30840;
  --shadow: 0 1px 3px rgba(0,0,0,0.3);
  --code-bg: #0f1117;
  --code-color: #3b4fd8;
}

[data-theme="light"] {
  --bg: #f5f5f7;
  --bg-card: #ffffff;
  --bg-input: #f0f1f4;
  --bg-hover: #eaebf0;
  --border: #d1d5db;
  --border-hover: #9ca3af;
  --text: #1f2937;
  --text-muted: #6b7280;
  --text-dim: #9ca3af;
  --accent: #3b4fd8;
  --accent-hover: #2f3fc0;
  --green: #16a34a;
  --red: #dc2626;
  --yellow: #ca8a04;
  --green-bg: #dcfce7;
  --red-bg: #fee2e2;
  --yellow-bg: #fef9c3;
  --green-border: #86efac;
  --red-border: #fca5a5;
  --yellow-border: #fde047;
  --shadow: 0 1px 3px rgba(0,0,0,0.08);
  --code-bg: #f0f1f4;
  --code-color: #3b4fd8;
}

/* ===== Base reset ===== */
* { margin: 0; padding: 0; box-sizing: border-box; }
body {
  font-family: system-ui, -apple-system, sans-serif;
  background: var(--bg);
  color: var(--text);
  min-height: 100vh;
  transition: background 0.2s, color 0.2s;
}

/* ===== Top nav ===== */
.topnav {
  background: var(--bg-card);
  border-bottom: 1px solid var(--border);
  padding: 0 24px;
  display: flex;
  align-items: center;
  height: 56px;
}
.topnav .logo { font-size: 18px; font-weight: 700; color: var(--text); margin-right: 32px; }
.topnav .logo span { color: var(--accent); }
.topnav a {
  color: var(--text-muted);
  text-decoration: none;
  padding: 16px 16px;
  font-size: 14px;
  font-weight: 500;
  border-bottom: 2px solid transparent;
  transition: all 0.2s;
}
.topnav a:hover { color: var(--text); }
.topnav a.active { color: var(--text); border-bottom-color: var(--accent); }
.topnav .spacer { flex: 1; }

/* Theme toggle in nav */
.theme-toggle {
  width: 36px; height: 36px;
  border-radius: 8px;
  border: 1px solid var(--border);
  background: var(--bg);
  color: var(--text-muted);
  font-size: 16px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.2s;
}
.theme-toggle:hover { border-color: var(--accent); color: var(--accent); }

/* ===== Container ===== */
.container { max-width: 1200px; margin: 0 auto; padding: 32px 24px; }
.container.narrow { max-width: 800px; }

/* ===== Page title ===== */
.page-title { font-size: 24px; font-weight: 700; color: var(--text); margin-bottom: 8px; }
.page-subtitle { font-size: 14px; color: var(--text-muted); margin-bottom: 32px; }

/* ===== Cards ===== */
.card {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 24px;
  margin-bottom: 24px;
  box-shadow: var(--shadow);
}
.card h3 { font-size: 16px; color: var(--text); margin-bottom: 16px; }

/* ===== Buttons ===== */
.btn {
  padding: 8px 20px;
  border-radius: 8px;
  border: none;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  text-decoration: none;
  transition: all 0.2s;
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.btn-primary { background: var(--accent); color: #fff; }
.btn-primary:hover { background: var(--accent-hover); }
.btn-outline { background: transparent; color: var(--text-muted); border: 1px solid var(--border); }
.btn-outline:hover { border-color: var(--accent); color: var(--text); }
.btn-success { background: var(--green-bg); color: var(--green); border: 1px solid var(--green-border); }
.btn-danger { background: var(--red-bg); color: var(--red); border: 1px solid var(--red-border); }
.btn-danger:hover { opacity: 0.85; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }

/* ===== Status badges ===== */
.status-dot { width: 8px; height: 8px; border-radius: 50%; }
.status-dot.green { background: var(--green); box-shadow: 0 0 6px var(--green); }
.status-dot.red { background: var(--red); box-shadow: 0 0 6px var(--red); }
.status-dot.yellow { background: var(--yellow); box-shadow: 0 0 6px var(--yellow); }

.status-badge {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 8px 16px;
  border-radius: 20px;
  font-size: 14px;
  font-weight: 500;
}
.status-badge.connected { background: var(--green-bg); color: var(--green); border: 1px solid var(--green-border); }
.status-badge.disconnected { background: var(--red-bg); color: var(--red); border: 1px solid var(--red-border); }
.status-badge.connecting { background: var(--yellow-bg); color: var(--yellow); border: 1px solid var(--yellow-border); }

/* ===== Forms ===== */
.form-group { margin-bottom: 20px; }
.form-group label { display: block; font-size: 14px; color: var(--text-muted); margin-bottom: 6px; font-weight: 500; }
.form-group input {
  width: 100%;
  padding: 10px 14px;
  background: var(--bg-input);
  border: 1px solid var(--border);
  border-radius: 8px;
  color: var(--text);
  font-size: 14px;
  outline: none;
  transition: border-color 0.2s;
}
.form-group input:focus { border-color: var(--accent); }
.form-group .hint { font-size: 12px; color: var(--text-dim); margin-top: 4px; }

/* ===== Notice ===== */
.notice {
  background: var(--yellow-bg);
  border: 1px solid var(--yellow-border);
  border-radius: 8px;
  padding: 16px;
  color: var(--yellow);
  font-size: 14px;
  line-height: 1.5;
}

/* Links */
a { color: var(--accent); }
`

// sharedJS is the theme toggle logic, served at /static/theme.js.
const sharedJS = `
(function() {
  var saved = localStorage.getItem('crm-theme');
  if (saved === 'light') document.documentElement.setAttribute('data-theme', 'light');

  window.toggleTheme = function() {
    var current = document.documentElement.getAttribute('data-theme');
    if (current === 'light') {
      document.documentElement.removeAttribute('data-theme');
      localStorage.setItem('crm-theme', 'dark');
      updateToggleIcon('dark');
    } else {
      document.documentElement.setAttribute('data-theme', 'light');
      localStorage.setItem('crm-theme', 'light');
      updateToggleIcon('light');
    }
  };

  window.updateToggleIcon = function(theme) {
    var btn = document.getElementById('themeBtn');
    if (btn) btn.textContent = theme === 'light' ? '\u263E' : '\u2600';
  };

  document.addEventListener('DOMContentLoaded', function() {
    var theme = localStorage.getItem('crm-theme') || 'dark';
    updateToggleIcon(theme);
  });
})();
`

// navHTML returns the topnav for a given active page.
func navHTML(active string) string {
	pages := []struct{ href, label string }{
		{"/", "Dashboard"},
		{"/setup/whatsapp", "WhatsApp"},
		{"/setup/facebook", "Facebook"},
		{"/setup/knowledge", "Knowledge"},
	}
	nav := `<nav class="topnav"><div class="logo">Claude <span>Bridge</span></div>`
	for _, p := range pages {
		cls := ""
		if p.href == active {
			cls = ` class="active"`
		}
		nav += `<a href="` + p.href + `"` + cls + `>` + p.label + `</a>`
	}
	nav += `<div class="spacer"></div><button class="theme-toggle" id="themeBtn" onclick="toggleTheme()" title="Toggle light/dark theme">☀</button></nav>`
	return nav
}

// headHTML returns the <head> section with shared CSS/JS.
func headHTML(title string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>` + title + `</title>
<link rel="stylesheet" href="/static/theme.css">
<script src="/static/theme.js"></script>
`
}
