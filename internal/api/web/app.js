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

function snapshotChanges(snapshot, previous) {
  const identity = file => `${file.sourceKey || ''}\u0000${file.path}`;
  const previousFiles = new Map((previous?.files || []).map(file => [identity(file), file]));
  const changes = (snapshot.files || []).map(file => {
    const old = previousFiles.get(identity(file));
    previousFiles.delete(identity(file));
    return { path: file.path, size: file.size, state: !old ? 'added' : old.hash === file.hash ? 'unchanged' : 'changed' };
  });
  previousFiles.forEach(file => changes.push({ path: file.path, size: file.size, state: 'removed' }));
  return changes;
}

function snapshotDetails(snapshot, previous) {
  const changes = snapshotChanges(snapshot, previous);
  const changed = changes.filter(file => file.state !== 'unchanged').length;
  return `<details class="snapshot-details"><summary>${changed} changed · ${changes.length} total files${snapshot.registry ? ' · registry included' : ''}</summary><div class="file-list">${changes.length ? changes.map(file => `<div><span class="file-state ${file.state}">${escapeHTML(file.state)}</span><code>${escapeHTML(file.path)}</code><small>${formatBytes(file.size)}</small></div>`).join('') : '<div><span>No files; this snapshot contains registry data only.</span></div>'}</div></details>`;
}

function toast(message, error = false) {
  const element = document.createElement('div');
  element.className = `toast${error ? ' error' : ''}`;
  element.textContent = message;
  document.querySelector('#toast-region').append(element);
  setTimeout(() => element.remove(), 4500);
}

async function browseInto(input) {
  const selected = await api('/api/folder', { method: 'POST', body: '{}' });
  if (selected?.path) input.value = selected.path;
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
      <div class="actions"><button class="button" id="scan-games">Scan for games</button><button class="button primary" id="add-game">+ Add game</button></div>
    </header>
    ${state.games.length ? `<section class="games">${state.games.map(game => `
      <a class="game-card" href="#/games/${encodeURIComponent(game.id)}">
        <div class="cover">${cover(game)}<span class="badge"><span class="dot ${game.enabled ? '' : 'warn'}"></span>${game.enabled ? 'Watching' : 'Manual backups'}</span></div>
        <div class="card-body"><h2>${escapeHTML(game.displayName)}</h2><div class="card-meta"><span>${game.snapshotCount} snapshots</span><span>${game.syncEnabled ? 'R2 sync on' : 'Local only'}</span><span>${formatBytes(game.storedSize)}</span></div></div>
      </a>`).join('')}</section>` : `
      <section class="empty"><div><div class="empty-mark">⌁</div><h2>No games tied in yet</h2><p class="subtle">Select Scan for games to check Steam, Epic, GOG, and existing local saves. You can also add any save folder yourself.</p><button class="button primary" id="empty-add">Add a custom game</button></div></section>`}`;
  document.querySelector('#add-game').addEventListener('click', showAddGame);
  document.querySelector('#empty-add')?.addEventListener('click', showAddGame);
  document.querySelector('#scan-games').addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true; button.textContent = 'Scanning…';
    try { const result = await api('/api/discovery', { method: 'POST', body: '{}' }); await loadShared(); gamesPage(); toast(`Found ${result.discovery.steamInstalled} Steam installs; matched ${result.discovery.catalogMatched}`); }
    catch (error) { toast(error.message, true); button.disabled = false; button.textContent = 'Scan for games'; }
  });
}

async function gamePage(id) {
  setActiveNav('games');
  root.innerHTML = '<div class="skeleton"></div>';
  const [{ game, paths, registry, exclusions, policy }, snapshots] = await Promise.all([api(`/api/games/${encodeURIComponent(id)}`), api(`/api/games/${encodeURIComponent(id)}/snapshots`)]);
  root.innerHTML = `
    <a class="subtle" href="#/games">← All games</a>
    <section class="detail-head section">
      <div class="detail-cover">${cover(game, true)}</div>
      <div class="detail-copy">
        <p class="eyebrow">${escapeHTML(game.store)} ${game.storeId ? `· ${escapeHTML(game.storeId)}` : ''}</p>
        <h1>${escapeHTML(game.displayName)}</h1>
        <p class="subtle">${escapeHTML(game.notes || 'Watching for changes and preserving each distinct version.')}</p>
        <div class="actions"><button class="button primary" id="backup">Back up locally</button><button class="button" id="sync" ${game.syncEnabled ? '' : 'disabled'} title="${game.syncEnabled ? 'Upload pending snapshots to R2' : 'Enable R2 sync in Customize first'}">Sync pending</button><button class="button" id="edit">Customize</button></div>
        <div class="stats"><div class="stat"><strong>${snapshots.length}</strong><small>Snapshots</small></div><div class="stat"><strong>${formatBytes(game.storedSize)}</strong><small>Stored</small></div><div class="stat"><strong>${formatTime(game.lastChange)}</strong><small>Last changed</small></div><div class="stat"><strong>${formatTime(game.lastBackup)}</strong><small>Last backup</small></div></div>
      </div>
    </section>
    <section class="section"><div class="section-title"><h2>Backups</h2><span class="subtle">Newest first</span></div>
      <div class="panel">${snapshots.length ? snapshots.map((snapshot, index) => `<div class="snapshot-entry"><div class="row"><div><strong>${formatTime(snapshot.createdAt)}</strong><small>${snapshot.files.length} files · ${formatBytes(snapshot.originalSize)} original · device ${escapeHTML(snapshot.deviceId.slice(-8))}</small></div><div class="actions"><span class="state ${snapshot.remoteState === 'synced' ? '' : 'local'}">${escapeHTML(snapshot.remoteState)}</span><button class="button small restore" data-snapshot="${escapeHTML(snapshot.id)}">Restore</button><button class="button small delete-snapshot" data-snapshot="${escapeHTML(snapshot.id)}" data-remote="${snapshot.remoteState === 'synced'}">Delete</button></div></div>${snapshotDetails(snapshot, snapshots[index + 1])}</div>`).join('') : '<div class="row"><div><strong>No snapshots yet</strong><small>Use Back up locally, or let the watcher catch the next save.</small></div></div>'}</div>
    </section>
    <section class="section"><div class="section-title"><h2>Save locations</h2><button class="button small" id="add-path">+ Add location</button></div>
      <div class="panel">${paths.map(path => `<div class="row"><div><strong>${escapeHTML(path.resolved)}</strong><small>${escapeHTML(path.source)} · ${escapeHTML(path.template)}</small></div><div class="actions"><span class="state">${path.enabled ? 'Included' : 'Excluded'}</span><button class="button small toggle-path" data-path="${escapeHTML(path.id)}" data-enabled="${path.enabled}">${path.enabled ? 'Exclude' : 'Include'}</button>${path.source === 'custom' ? `<button class="button small remove-path" data-path="${escapeHTML(path.id)}">Remove</button>` : ''}</div></div>`).join('') || '<div class="row"><strong>No save locations configured</strong></div>'}</div>
    </section>
    ${registry?.length ? `<section class="section"><div class="section-title"><h2>Windows registry saves</h2><span class="state">Included</span></div><div class="panel">${registry.map(item => `<div class="row"><div><strong>${escapeHTML(item.path)}</strong><small>Recursive registry backup and restore</small></div></div>`).join('')}</div></section>` : ''}
    <section class="section"><div class="section-title"><h2>File exclusions</h2><button class="button small" id="add-exclusion">+ Add exclusion</button></div><div class="panel">${exclusions?.length ? exclusions.map(item => `<div class="row"><div><strong>${escapeHTML(item.pattern)}</strong><small>Glob matched against relative and absolute save paths</small></div><button class="button small remove-exclusion" data-exclusion="${escapeHTML(item.id)}">Remove</button></div>`).join('') : '<div class="row"><div><strong>No exclusions</strong><small>Examples: **/settings.json or **/*.tmp</small></div></div>'}</div></section>`;
  document.querySelector('#backup').addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/backup`, { method: 'POST', body: '{}' }); toast('Snapshot created'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#sync').addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/sync`, { method: 'POST', body: '{}' }); toast('Pending snapshots synced'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#edit').addEventListener('click', () => showEditGame(game, policy));
  document.querySelector('#add-path').addEventListener('click', () => showAddPath(game));
  document.querySelector('#add-exclusion').addEventListener('click', () => showAddExclusion(game));
  document.querySelectorAll('.toggle-path').forEach(button => button.addEventListener('click', async () => {
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/paths/${encodeURIComponent(button.dataset.path)}`, { method: 'PATCH', body: JSON.stringify({ enabled: button.dataset.enabled !== 'true' }) }); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  document.querySelectorAll('.remove-path').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('Remove this custom save location? Existing snapshots are not deleted.')) return;
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/paths/${encodeURIComponent(button.dataset.path)}`, { method: 'DELETE' }); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  document.querySelectorAll('.remove-exclusion').forEach(button => button.addEventListener('click', async () => {
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/exclusions/${encodeURIComponent(button.dataset.exclusion)}`, { method: 'DELETE' }); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  document.querySelectorAll('.restore').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('Restore this version? SaveKnot will snapshot your current files first.')) return;
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/restore/${encodeURIComponent(button.dataset.snapshot)}`, { method: 'POST', body: '{}' }); toast('Save restored; previous files were preserved as a snapshot'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  document.querySelectorAll('.delete-snapshot').forEach(button => button.addEventListener('click', async () => {
    const scope = button.dataset.remote === 'true' ? 'locally and from R2' : 'locally';
    if (!confirm(`Permanently delete this snapshot ${scope}? Shared content blobs are retained safely.`)) return;
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/snapshots/${encodeURIComponent(button.dataset.snapshot)}?remote=${button.dataset.remote}`, { method: 'DELETE' }); toast('Snapshot deleted'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
}

function settingsPage() {
  setActiveNav('settings');
  const r2 = state.status.r2 || {};
  const diagnostics = state.status.diagnostics || {};
  const catalog = diagnostics.catalog || {};
  const discovery = diagnostics.discovery || {};
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
      <form class="settings-card" id="local-form"><h2>Local storage & discovery</h2><p class="subtle">Choose where compressed local blobs live and optionally add Steam installation roots.</p>
        <div class="form-grid">
          <div class="field full"><label for="local-backups">Local backup location</label><div class="actions"><input id="local-backups" name="localBackupDir" required value="${escapeHTML(state.status.localBackupDir || '')}"><button class="button browse" type="button" data-target="local-backups">Browse…</button></div></div>
          <div class="field full"><label for="steam-roots">Extra Steam roots</label><textarea id="steam-roots" name="steamRoots" placeholder="One absolute folder per line">${escapeHTML((state.status.steamRoots || []).join('\n'))}</textarea><span class="help">SaveKnot also checks Steam's Windows registry entries and standard locations automatically.</span></div>
          <div class="field full"><label for="epic-manifests">Extra Epic manifest folders</label><textarea id="epic-manifests" name="epicManifests" placeholder="One absolute folder per line">${escapeHTML((state.status.epicManifests || []).join('\n'))}</textarea></div>
          <div class="field full"><label for="gog-roots">Extra GOG game roots</label><textarea id="gog-roots" name="gogRoots" placeholder="One absolute folder per line">${escapeHTML((state.status.gogRoots || []).join('\n'))}</textarea></div>
          <div class="field full"><label><input type="checkbox" name="launchAtLogin" ${state.status.launchAtLogin ? 'checked' : ''}> Launch SaveKnot when I sign in</label></div>
          <div class="field"><label for="retention">Snapshots kept per game</label><input id="retention" type="number" min="1" max="10000" name="retentionKeep" value="${escapeHTML(state.status.retentionKeep || 50)}"></div>
        </div><div class="form-actions"><button class="button primary">Save local settings</button></div>
      </form>
      <div class="settings-card"><h2>Discovery diagnostics</h2><p class="subtle">Use these counts to diagnose an empty game list.</p>
        <div class="callout">Catalog: ${catalog.loaded ? `${catalog.gameCount || 0} games loaded` : `not loaded${catalog.lastError ? ` · ${escapeHTML(catalog.lastError)}` : ''}`}<br>Steam roots: ${(discovery.steamRoots || []).length ? discovery.steamRoots.map(escapeHTML).join(', ') : 'none detected'}<br>Installed: ${discovery.steamInstalled || 0} Steam · ${discovery.epicInstalled || 0} Epic · ${discovery.gogInstalled || 0} GOG<br>Matched in Ludusavi: ${discovery.catalogMatched || 0}<br>Found from local save data: ${discovery.localSaveGames || 0}${discovery.deepScanMillis ? ` (${discovery.deepScanMillis} ms deep scan)` : ''}<br>Registered: ${discovery.gamesRegistered || 0}${(discovery.unmatched || []).length ? `<br>Unmatched: ${discovery.unmatched.slice(0, 10).map(escapeHTML).join(', ')}` : ''}${discovery.lastError ? `<br>Error: ${escapeHTML(discovery.lastError)}` : ''}</div>
        <div class="form-actions"><button class="button" type="button" id="settings-scan">Scan now</button>${state.status.r2Configured ? '<button class="button" type="button" id="reconcile-r2">Refresh from R2</button><button class="button" type="button" id="sync-all">Sync all pending</button>' : ''}</div>
      </div>
      <div class="settings-card"><h2>No middleman</h2><p class="subtle">There is no SaveKnot account, hosted API, telemetry collector, or central database.</p><div class="callout">The local daemon uploads content-addressed blobs first and publishes an immutable snapshot manifest last. An interrupted upload cannot create a valid partial backup.</div></div>
    </section>`;
  document.querySelector('#r2-form').addEventListener('submit', async event => {
    event.preventDefault(); const button = event.currentTarget.querySelector('button'); button.disabled = true; button.textContent = 'Connecting…';
    try { const data = Object.fromEntries(new FormData(event.currentTarget)); await api('/api/r2', { method: 'POST', body: JSON.stringify(data) }); toast('R2 connection verified and saved'); await loadShared(); settingsPage(); }
    catch (error) { toast(error.message, true); button.disabled = false; button.textContent = 'Test & connect'; }
  });
  document.querySelector('#local-form').addEventListener('submit', async event => {
    event.preventDefault(); const form = new FormData(event.currentTarget); const button = event.currentTarget.querySelector('button[type="submit"]'); button.disabled = true;
    const lines = name => String(form.get(name) || '').split(/\r?\n/).map(value => value.trim()).filter(Boolean);
    try { await api('/api/settings/local', { method: 'PUT', body: JSON.stringify({ localBackupDir: form.get('localBackupDir'), steamRoots: lines('steamRoots'), epicManifests: lines('epicManifests'), gogRoots: lines('gogRoots') }) }); await api('/api/settings/autostart', { method: 'PUT', body: JSON.stringify({ enabled: form.get('launchAtLogin') === 'on' }) }); await api('/api/settings/retention', { method: 'PUT', body: JSON.stringify({ keep: Number(form.get('retentionKeep')) }) }); await loadShared(); settingsPage(); toast('Local settings saved; select Scan now to rediscover games'); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelectorAll('.browse').forEach(button => button.addEventListener('click', async () => {
    button.disabled = true;
    try { await browseInto(document.querySelector(`#${button.dataset.target}`)); }
    catch (error) { toast(error.message, true); }
    finally { button.disabled = false; }
  }));
  document.querySelector('#settings-scan').addEventListener('click', async event => {
    event.currentTarget.disabled = true;
    try { await api('/api/discovery', { method: 'POST', body: '{}' }); await loadShared(); settingsPage(); toast('Discovery scan completed'); }
    catch (error) { toast(error.message, true); event.currentTarget.disabled = false; }
  });
  document.querySelector('#sync-all')?.addEventListener('click', async event => {
    event.currentTarget.disabled = true;
    try { await api('/api/sync', { method: 'POST', body: '{}' }); await loadShared(); settingsPage(); toast('All pending snapshots synced'); }
    catch (error) { toast(error.message, true); event.currentTarget.disabled = false; }
  });
  document.querySelector('#reconcile-r2')?.addEventListener('click', async event => {
    event.currentTarget.disabled = true;
    try { const result = await api('/api/r2/reconcile', { method: 'POST', body: '{}' }); await loadShared(); settingsPage(); toast(`Loaded ${result.snapshots} new snapshots; skipped ${result.skipped} already known`); }
    catch (error) { toast(error.message, true); event.currentTarget.disabled = false; }
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
  const body = element.querySelector('.modal'); body.insertAdjacentHTML('beforeend', `<form id="game-form"><div class="form-grid"><div class="field full"><label>Name</label><input name="name" required autofocus></div><div class="field full"><label>Save location</label><div class="actions"><input id="new-game-path" name="path" required placeholder="Choose a save folder"><button class="button browse-path" type="button">Browse…</button></div><span class="help">Choose a folder or enter an absolute path. More locations can be added afterward.</span></div></div><div class="form-actions"><button class="button primary" type="submit">Add game</button></div></form>`);
  body.querySelector('.browse-path').addEventListener('click', async event => { event.currentTarget.disabled = true; try { await browseInto(body.querySelector('#new-game-path')); } catch (error) { toast(error.message, true); } finally { event.currentTarget.disabled = false; } });
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); const form = new FormData(event.currentTarget); try { const game = await api('/api/games', { method: 'POST', body: JSON.stringify({ name: form.get('name'), paths: [form.get('path')] }) }); element.remove(); await loadShared(); location.hash = `#/games/${game.id}`; } catch (error) { toast(error.message, true); } });
}

function showAddPath(game) {
  const element = modal(`<div><p class="eyebrow">${escapeHTML(game.displayName)}</p><h2>Add save location</h2></div>`); const body = element.querySelector('.modal');
  body.insertAdjacentHTML('beforeend', `<form><div class="field"><label>Save folder</label><div class="actions"><input name="path" required autofocus><button class="button browse-path" type="button">Browse…</button></div></div><div class="form-actions"><button class="button primary" type="submit">Add location</button></div></form>`);
  body.querySelector('.browse-path').addEventListener('click', async event => { event.currentTarget.disabled = true; try { await browseInto(body.querySelector('input[name="path"]')); } catch (error) { toast(error.message, true); } finally { event.currentTarget.disabled = false; } });
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); try { await api(`/api/games/${encodeURIComponent(game.id)}/paths`, { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(event.currentTarget))) }); element.remove(); gamePage(game.id); } catch (error) { toast(error.message, true); } });
}

function showAddExclusion(game) {
  const element = modal(`<div><p class="eyebrow">${escapeHTML(game.displayName)}</p><h2>Exclude files</h2><p class="subtle">Use a glob such as **/settings.json or **/*.tmp.</p></div>`); const body = element.querySelector('.modal');
  body.insertAdjacentHTML('beforeend', `<form><div class="field"><label>Glob pattern</label><input name="pattern" required autofocus placeholder="**/*.tmp"></div><div class="form-actions"><button class="button primary" type="submit">Add exclusion</button></div></form>`);
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); try { await api(`/api/games/${encodeURIComponent(game.id)}/exclusions`, { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(event.currentTarget))) }); element.remove(); gamePage(game.id); } catch (error) { toast(error.message, true); } });
}

function showEditGame(game, policy) {
  const element = modal(`<div><p class="eyebrow">Customize</p><h2>${escapeHTML(game.displayName)}</h2></div>`); const body = element.querySelector('.modal');
  body.insertAdjacentHTML('beforeend', `<form id="edit-form"><div class="form-grid"><div class="field full"><label>Display name</label><input name="displayName" required value="${escapeHTML(game.displayName)}"></div><div class="field full"><label>Ludusavi catalog mapping</label><input name="catalogId" list="catalog-options" value="${escapeHTML(game.catalogId || '')}" placeholder="Search catalog title"><datalist id="catalog-options"></datalist><span class="help">Change this only when SaveKnot matched the wrong game.</span></div><div class="field full"><label>Notes</label><textarea name="notes">${escapeHTML(game.notes || '')}</textarea></div><div class="field full"><label><input type="checkbox" name="enabled" ${game.enabled ? 'checked' : ''}> Watch for changes and create automatic local backups</label></div><div class="field full"><label><input type="checkbox" name="syncEnabled" ${game.syncEnabled ? 'checked' : ''}> Sync this game's pending snapshots to R2</label></div><div class="field"><label>Quiet debounce (seconds)</label><input type="number" min="1" name="quietSeconds" value="${policy.quietSeconds}"></div><div class="field"><label>Minimum snapshot gap (seconds)</label><input type="number" min="0" name="minGapSeconds" value="${policy.minGapSeconds}"></div><div class="field"><label>Maximum dirty duration (seconds)</label><input type="number" min="1" name="maxDirtySeconds" value="${policy.maxDirtySeconds}"></div><div class="field full"><label>Custom picture</label><input type="file" name="image" accept="image/png,image/jpeg,image/gif,image/webp"></div></div><div class="form-actions"><button class="button primary">Save changes</button></div></form>`);
  const catalogInput = body.querySelector('input[name="catalogId"]'); let catalogTimer;
  const suggestCatalog = () => { clearTimeout(catalogTimer); catalogTimer = setTimeout(async () => { try { const choices = await api(`/api/catalog?q=${encodeURIComponent(catalogInput.value)}`); body.querySelector('#catalog-options').innerHTML = choices.map(choice => `<option value="${escapeHTML(choice.id)}"></option>`).join(''); } catch { /* Suggestions are optional; submit still validates the mapping. */ } }, 200); };
  catalogInput.addEventListener('input', suggestCatalog); suggestCatalog();
  body.querySelector('form').addEventListener('submit', async event => {
    event.preventDefault(); const form = new FormData(event.currentTarget);
    try {
      await api(`/api/games/${encodeURIComponent(game.id)}`, { method: 'PATCH', body: JSON.stringify({ displayName: form.get('displayName'), notes: form.get('notes'), enabled: form.get('enabled') === 'on', syncEnabled: form.get('syncEnabled') === 'on' }) });
      if (form.get('catalogId') && form.get('catalogId') !== game.catalogId) await api(`/api/games/${encodeURIComponent(game.id)}/remap`, { method: 'PUT', body: JSON.stringify({ catalogId: form.get('catalogId') }) });
      await api(`/api/games/${encodeURIComponent(game.id)}/policy`, { method: 'PUT', body: JSON.stringify({ quietSeconds: Number(form.get('quietSeconds')), minGapSeconds: Number(form.get('minGapSeconds')), maxDirtySeconds: Number(form.get('maxDirtySeconds')) }) });
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
