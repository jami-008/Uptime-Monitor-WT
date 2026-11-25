'use strict';

const $ = (id) => document.getElementById(id);
let sites = [];
let historyEntries = [];
let notificationTimer;
let requestPending = false;

function escapeHtml(value) {
  return String(value ?? '').replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[ch]);
}
function shortTime(iso) {
  if (!iso || iso.startsWith('0001-')) return 'Not checked yet';
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? 'Unknown' : d.toLocaleString();
}
function percentage(site) {
  return site.total_checks ? (site.successful_checks / site.total_checks * 100).toFixed(1) + '%' : '—';
}
function statusText(s) {
  return s === 'up' ? 'UP' : s === 'down' ? 'DOWN' : 'PENDING';
}
function toast(message, isError = false) {
  const target = $('toast');
  target.textContent = message;
  target.className = 'toast visible' + (isError ? ' error' : '');
  clearTimeout(notificationTimer);
  notificationTimer = setTimeout(() => target.className = 'toast', 3900);
}
async function api(path, options = {}) {
  const response = await fetch(path, { cache: 'no-store', ...options });
  let value;
  try { value = await response.json(); } catch { value = null; }
  if (!response.ok) throw new Error(value?.error || `HTTP ${response.status}`);
  return value;
}
function renderSummary() {
  $('total-count').textContent = sites.length;
  $('up-count').textContent = sites.filter(site => site.status === 'up').length;
  $('down-count').textContent = sites.filter(site => site.status === 'down').length;
  const totalChecks = sites.reduce((n, s) => n + s.total_checks, 0);
  const successes = sites.reduce((n, s) => n + s.successful_checks, 0);
  $('uptime-value').textContent = totalChecks ? (100 * successes / totalChecks).toFixed(1) + '%' : '—';
}
function renderSites() {
  if (sites.length === 0) {
    $('monitor-list').innerHTML = '<div class="empty-state"><div class="empty-icon">⌁</div><h3>No monitors yet</h3><p>Add a website to begin tracking uptime automatically.</p></div>';
    return;
  }
  $('monitor-list').innerHTML = sites.map(site => {
    const id = escapeHtml(site.id);
    const status = site.status === 'up' ? 'up' : site.status === 'down' ? 'down' : '';
    const error = site.last_error ? `<div class="check-error">${escapeHtml(site.last_error)}</div>` : '';
    return `<article class="monitor-card">
      <div class="monitor-top"><div><div class="monitor-name">${escapeHtml(site.name)}</div><span class="monitor-url">${escapeHtml(site.url)}</span></div><span class="status-pill ${status}">${statusText(site.status)}</span></div>
      <div class="monitor-stats">
        <span class="mini-stat"><b>${percentage(site)}</b>UPTIME</span>
        <span class="mini-stat"><b>${site.total_checks ? `${site.response_time_ms} ms` : '—'}</b>RESPONSE</span>
        <span class="mini-stat"><b>${site.http_status || '—'}</b>HTTP STATUS</span>
        <span class="mini-stat"><b>${site.interval_seconds}s</b>INTERVAL</span>
      </div>
      ${error}
      <div class="monitor-bottom"><span class="check-time">${site.checking ? 'Checking now…' : 'Last checked: ' + escapeHtml(shortTime(site.last_checked_at))}</span>
        <div class="action-buttons"><button class="tiny-btn" data-action="check" data-id="${id}" ${site.checking ? 'disabled' : ''}>↻ Check now</button><button class="tiny-btn danger" data-action="delete" data-id="${id}">Remove</button></div></div>
    </article>`;
  }).join('');
}
function renderHistory() {
  if (!historyEntries.length) {
    $('activity-body').innerHTML = '<tr><td colspan="4" class="table-empty">No checks recorded yet. New monitors will be checked automatically.</td></tr>';
    return;
  }
  $('activity-body').innerHTML = historyEntries.map(item => `<tr>
    <td><span class="activity-site">${escapeHtml(item.site_name)}</span><span class="activity-url">${escapeHtml(item.url)}</span></td>
    <td><span class="result ${item.ok ? '' : 'failed'}"><span class="dot"></span>${item.ok ? 'Success' : 'Failed'}</span>${item.state_change ? `<span class="state-note">${escapeHtml(item.state_change.toUpperCase())}</span>` : ''}</td>
    <td>${item.response_time_ms} ms ${item.http_status ? `<span style="color:#778baa">· HTTP ${item.http_status}</span>` : ''}</td>
    <td>${escapeHtml(shortTime(item.at))}</td>
  </tr>`).join('');
}
async function refresh() {
  if (requestPending) return;
  requestPending = true;
  try {
    const [list, events] = await Promise.all([api('/api/sites'), api('/api/history')]);
    sites = list;
    historyEntries = events;
    renderSummary(); renderSites(); renderHistory();
  } catch (err) {
    toast('Could not reach monitoring server: ' + err.message, true);
  } finally { requestPending = false; }
}
$('add-form').addEventListener('submit', async event => {
  event.preventDefault();
  const button = $('add-button');
  button.disabled = true;
  const payload = { name: $('site-name').value.trim(), url: $('site-url').value.trim(), interval_seconds: Number($('site-interval').value) };
  try {
    await api('/api/sites', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
    $('site-name').value = ''; $('site-url').value = '';
    toast('Monitor created! First check will start shortly.');
    await refresh();
  } catch (err) { toast(err.message, true); }
  finally { button.disabled = false; }
});
$('monitor-list').addEventListener('click', async event => {
  const btn = event.target.closest('button[data-action]');
  if (!btn) return;
  const id = btn.dataset.id;
  const action = btn.dataset.action;
  if (action === 'delete' && !window.confirm('Remove this monitor?')) return;
  btn.disabled = true;
  try {
    if (action === 'check') {
      await api(`/api/sites/${encodeURIComponent(id)}/check`, { method: 'POST' });
      toast('Check started.');
    } else if (action === 'delete') {
      const response = await fetch(`/api/sites/${encodeURIComponent(id)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error('Could not remove monitor');
      toast('Monitor removed.');
    }
    await refresh();
  } catch (err) { toast(err.message, true); btn.disabled = false; }
});
refresh();
setInterval(refresh, 5000);
