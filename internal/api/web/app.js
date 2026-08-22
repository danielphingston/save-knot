const state = { games: [], status: null, activity: [] };
const root = document.querySelector('#app');

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: options.body instanceof FormData ? options.headers : { 'Content-Type': 'application/json', ...(options.headers || {}) },
  });
  if (response.status === 204) return null;
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`);
  return data;
}

const escapeHTML = value => String(value ?? '').replace(/[&<>'"]/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#039;', '"': '&quot;' })[character]);
const formatBytes = bytes => {
  if (!bytes) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const position = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** position).toFixed(position ? 1 : 0)} ${units[position]}`;
};
const formatTime = value => value ? new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : 'Never';
const initial = name => escapeHTML((name || '?').trim().charAt(0).toUpperCase());
const cover = (game, detail = false) => game.image
  ? `<img src="${escapeHTML(game.image)}" alt="${escapeHTML(game.displayName)} artwork">`
  : `<span class="cover-fallback" aria-hidden="true">${initial(game.displayName)}</span>`;

function toast(message, error = false) {
  const element = document.createElement('div');
  element.className = `toast${error ? ' error' : ''}`;
  element.textContent = message;
  document.querySelector('#toast-region').append(element);
  setTimeout(() => element.remove(), 4500);
}

function setActiveNav(route) {
  document.querySelectorAll('nav a').forEach(link => link.classList.toggle('active', link.dataset.route === route));
}

async function loadShared() {
  [state.games, state.status] = await Promise.all([api('/api/games'), api('/api/status')]);
  const configured = state.status.r2Configured;
  document.querySelector('#connection-dot').classList.toggle('warn', !configured);
  document.querySelector('#connection-title').textContent = configured ? 'R2 connected' : 'Local only';
  document.querySelector('#connection-detail').textContent = configured ? state.status.r2.bucket : 'R2 not connected';
}

function gamesPage() {
  setActiveNav('games');
  root.innerHTML = `
    <header class="page-head">
      <div><p class="eyebrow">Your library</p><h1>Game saves</h1><p class="subtle">Immutable checkpoints, kept close and synced safely.</p></div>
      <button class="button primary" id="add-game">+ Add game</button>
    </header>
    ${state.games.length ? `<section class="games">${state.games.map(game => `
      <a class="game-card" href="#/games/${encodeURIComponent(game.id)}">
        <div class="cover">${cover(game)}<span class="badge"><span class="dot ${game.enabled ? '' : 'warn'}"></span>${game.enabled ? 'Watching' : 'Paused'}</span></div>
        <div class="card-body"><h2>${escapeHTML(game.displayName)}</h2><div class="card-meta"><span>${game.snapshotCount} snapshots</span><span>${formatBytes(game.storedSize)}</span></div></div>
      </a>`).join('')}</section>` : `
      <section class="empty"><div><div class="empty-mark">⌁</div><h2>No games tied in yet</h2><p class="subtle">Steam games appear automatically after the Ludusavi catalog loads. You can also add any save folder yourself.</p><button class="button primary" id="empty-add">Add a custom game</button></div></section>`}`;
  document.querySelector('#add-game').addEventListener('click', showAddGame);
  document.querySelector('#empty-add')?.addEventListener('click', showAddGame);
}

async function gamePage(id) {
  setActiveNav('games');
  root.innerHTML = '<div class="skeleton"></div>';
  const [{ game, paths }, snapshots] = await Promise.all([api(`/api/games/${encodeURIComponent(id)}`), api(`/api/games/${encodeURIComponent(id)}/snapshots`)]);
  root.innerHTML = `
    <a class="subtle" href="#/games">← All games</a>
    <section class="detail-head section">
      <div class="detail-cover">${cover(game, true)}</div>
      <div class="detail-copy">
        <p class="eyebrow">${escapeHTML(game.store)} ${game.storeId ? `· ${escapeHTML(game.storeId)}` : ''}</p>
        <h1>${escapeHTML(game.displayName)}</h1>
        <p class="subtle">${escapeHTML(game.notes || 'Watching for changes and preserving each distinct version.')}</p>
        <div class="actions"><button class="button primary" id="backup">Back up now</button><button class="button" id="edit">Customize</button></div>
        <div class="stats"><div class="stat"><strong>${snapshots.length}</strong><small>Snapshots</small></div><div class="stat"><strong>${formatBytes(game.storedSize)}</strong><small>Stored</small></div><div class="stat"><strong>${formatTime(game.lastBackup)}</strong><small>Last backup</small></div></div>
      </div>
    </section>
    <section class="section"><div class="section-title"><h2>Backups</h2><span class="subtle">Newest first</span></div>
      <div class="panel">${snapshots.length ? snapshots.map(snapshot => `<div class="row"><div><strong>${formatTime(snapshot.createdAt)}</strong><small>${snapshot.files.length} files · ${formatBytes(snapshot.originalSize)} original · ${escapeHTML(snapshot.deviceId.slice(-8))}</small></div><div class="actions"><span class="state ${snapshot.remoteState === 'synced' ? '' : 'local'}">${escapeHTML(snapshot.remoteState)}</span><button class="button small restore" data-snapshot="${escapeHTML(snapshot.id)}">Restore</button></div></div>`).join('') : '<div class="row"><div><strong>No snapshots yet</strong><small>Use Back up now, or let the watcher catch the next save.</small></div></div>'}</div>
    </section>
    <section class="section"><div class="section-title"><h2>Save locations</h2><button class="button small" id="add-path">+ Add location</button></div>
      <div class="panel">${paths.map(path => `<div class="row"><div><strong>${escapeHTML(path.resolved)}</strong><small>${escapeHTML(path.source)} · ${escapeHTML(path.template)}</small></div><span class="state">${path.enabled ? 'Included' : 'Excluded'}</span></div>`).join('') || '<div class="row"><strong>No save locations configured</strong></div>'}</div>
    </section>`;
  document.querySelector('#backup').addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/backup`, { method: 'POST', body: '{}' }); toast('Snapshot created'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#edit').addEventListener('click', () => showEditGame(game));
  document.querySelector('#add-path').addEventListener('click', () => showAddPath(game));
  document.querySelectorAll('.restore').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('Restore this version? SaveKnot will snapshot your current files first.')) return;
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/restore/${encodeURIComponent(button.dataset.snapshot)}`, { method: 'POST', body: '{}' }); toast('Save restored; previous files were preserved as a snapshot'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
}

function settingsPage() {
  setActiveNav('settings');
  const r2 = state.status.r2 || {};
  root.innerHTML = `
    <header class="page-head"><div><p class="eyebrow">Private by design</p><h1>Storage</h1><p class="subtle">SaveKnot connects straight from this device to your bucket.</p></div></header>
    <section class="settings-grid">
      <form class="settings-card" id="r2-form"><h2>Cloudflare R2</h2><p class="subtle">Use a bucket-scoped token with Object Read & Write permission.</p>
        <div class="form-grid">
          <div class="field"><label for="account">Account ID</label><input id="account" name="accountId" required value="${escapeHTML(r2.accountId || '')}"></div>
          <div class="field"><label for="bucket">Bucket</label><input id="bucket" name="bucket" required value="${escapeHTML(r2.bucket || '')}"></div>
          <div class="field full"><label for="key">Access key ID</label><input id="key" name="accessKeyId" required autocomplete="off" value="${escapeHTML(r2.accessKeyId || '')}"></div>
          <div class="field full"><label for="secret">Secret access key</label><input id="secret" type="password" name="secretAccessKey" required autocomplete="new-password"><span class="help">Stored in your operating system credential vault, never in SQLite or config.json.</span></div>
          <div class="field full"><label for="prefix">Object prefix</label><input id="prefix" name="prefix" value="${escapeHTML(r2.prefix || 'saveknot')}"></div>
        </div><div class="form-actions"><button class="button primary">Test & connect</button></div>
      </form>
      <div class="settings-card"><h2>No middleman</h2><p class="subtle">There is no SaveKnot account, hosted API, telemetry collector, or central database.</p><div class="callout">The local daemon uploads content-addressed blobs first and publishes an immutable snapshot manifest last. An interrupted upload cannot create a valid partial backup.</div></div>
    </section>`;
  document.querySelector('#r2-form').addEventListener('submit', async event => {
    event.preventDefault(); const button = event.currentTarget.querySelector('button'); button.disabled = true; button.textContent = 'Connecting…';
    try { const data = Object.fromEntries(new FormData(event.currentTarget)); await api('/api/r2', { method: 'POST', body: JSON.stringify(data) }); toast('R2 connection verified and saved'); await loadShared(); settingsPage(); }
    catch (error) { toast(error.message, true); button.disabled = false; button.textContent = 'Test & connect'; }
  });
}

function activityPage() {
  setActiveNav('activity');
  root.innerHTML = `<header class="page-head"><div><p class="eyebrow">Live from this device</p><h1>Activity</h1><p class="subtle">Catalog, watcher, snapshot, restore, and upload events.</p></div></header><div class="panel" id="activity-list">${state.activity.length ? state.activity.map(activityRow).join('') : '<div class="row"><div><strong>Quiet for now</strong><small>New background events will appear here while this page is open.</small></div></div>'}</div>`;
}

function activityRow(event) {
  return `<div class="row"><div><strong>${escapeHTML(event.type.replaceAll('.', ' · '))}</strong><small>${formatTime(event.timestamp)}${event.message ? ` · ${escapeHTML(event.message)}` : ''}</small></div>${event.gameId ? `<a class="button small" href="#/games/${encodeURIComponent(event.gameId)}">View</a>` : ''}</div>`;
}

function modal(content) {
  const backdrop = document.createElement('div'); backdrop.className = 'modal-backdrop'; backdrop.innerHTML = `<div class="modal"><div class="modal-head">${content}<button class="close" aria-label="Close">×</button></div></div>`;
  backdrop.querySelector('.close').addEventListener('click', () => backdrop.remove());
  backdrop.addEventListener('click', event => { if (event.target === backdrop) backdrop.remove(); });
  document.body.append(backdrop); return backdrop;
}

function showAddGame() {
  const element = modal(`<div><p class="eyebrow">Manual entry</p><h2>Add a game</h2><p class="subtle">Point SaveKnot at an absolute save folder or file.</p></div>`);
  const body = element.querySelector('.modal'); body.insertAdjacentHTML('beforeend', `<form id="game-form"><div class="form-grid"><div class="field full"><label>Name</label><input name="name" required autofocus></div><div class="field full"><label>Save location</label><input name="path" required placeholder="/home/me/.local/share/Game/Saves"><span class="help">Use an absolute path. More locations can be added afterward.</span></div></div><div class="form-actions"><button class="button primary">Add game</button></div></form>`);
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); const form = new FormData(event.currentTarget); try { const game = await api('/api/games', { method: 'POST', body: JSON.stringify({ name: form.get('name'), paths: [form.get('path')] }) }); element.remove(); await loadShared(); location.hash = `#/games/${game.id}`; } catch (error) { toast(error.message, true); } });
}

function showAddPath(game) {
  const element = modal(`<div><p class="eyebrow">${escapeHTML(game.displayName)}</p><h2>Add save location</h2></div>`); const body = element.querySelector('.modal');
  body.insertAdjacentHTML('beforeend', `<form><div class="field"><label>Absolute path</label><input name="path" required autofocus></div><div class="form-actions"><button class="button primary">Add location</button></div></form>`);
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); try { await api(`/api/games/${encodeURIComponent(game.id)}/paths`, { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(event.currentTarget))) }); element.remove(); gamePage(game.id); } catch (error) { toast(error.message, true); } });
}

function showEditGame(game) {
  const element = modal(`<div><p class="eyebrow">Customize</p><h2>${escapeHTML(game.displayName)}</h2></div>`); const body = element.querySelector('.modal');
  body.insertAdjacentHTML('beforeend', `<form id="edit-form"><div class="form-grid"><div class="field full"><label>Display name</label><input name="displayName" required value="${escapeHTML(game.displayName)}"></div><div class="field full"><label>Notes</label><textarea name="notes">${escapeHTML(game.notes || '')}</textarea></div><div class="field full"><label><input type="checkbox" name="enabled" ${game.enabled ? 'checked' : ''}> Watch and sync this game</label></div><div class="field full"><label>Custom picture</label><input type="file" name="image" accept="image/png,image/jpeg,image/gif,image/webp"></div></div><div class="form-actions"><button class="button primary">Save changes</button></div></form>`);
  body.querySelector('form').addEventListener('submit', async event => {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    try {
      await api(`/api/games/${encodeURIComponent(game.id)}`, { method: 'PATCH', body: JSON.stringify({ displayName: form.get('displayName'), notes: form.get('notes'), enabled: form.get('enabled') === 'on' }) });
      const image = form.get('image'); if (image?.size) { const upload = new FormData(); upload.set('image', image); await api(`/api/games/${encodeURIComponent(game.id)}/image`, { method: 'POST', body: upload }); }
      element.remove(); await loadShared(); gamePage(game.id);
    } catch (error) { toast(error.message, true); }
  });
}

async function route() {
  try {
    await loadShared();
    const parts = (location.hash || '#/games').slice(2).split('/');
    if (parts[0] === 'settings') settingsPage();
    else if (parts[0] === 'activity') activityPage();
    else if (parts[0] === 'games' && parts[1]) await gamePage(decodeURIComponent(parts[1]));
    else gamesPage();
    root.focus({ preventScroll: true });
  } catch (error) {
    root.innerHTML = `<section class="empty"><div><div class="empty-mark">!</div><h2>SaveKnot could not load</h2><p class="subtle">${escapeHTML(error.message)}</p><button class="button" id="retry">Try again</button></div></section>`;
    document.querySelector('#retry').addEventListener('click', route);
  }
}

const stream = new EventSource('/api/events');
stream.onmessage = message => {
  const event = JSON.parse(message.data); state.activity.unshift(event); state.activity = state.activity.slice(0, 100);
  if (location.hash === '#/activity') activityPage();
  if (['snapshot.completed', 'restore.completed', 'game.discovered'].includes(event.type)) loadShared().catch(() => {});
};

window.addEventListener('hashchange', route);
route();
