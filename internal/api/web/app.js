const state = {
  games: [], ignoredGames: [], status: null, activity: [],
  gameFilters: { query: '', watcher: 'all', sync: 'all', snapshots: 'all' },
};
const root = document.querySelector('#app');

async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    cache: 'no-store',
    headers: options.body instanceof FormData ? options.headers : { 'Content-Type': 'application/json', ...(options.headers || {}) },
  });
  if (response.status === 204) return null;
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(data.error || `Request failed (${response.status})`);
    error.status = response.status;
    throw error;
  }
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
  [state.games, state.ignoredGames, state.status] = await Promise.all([api('/api/games'), api('/api/games/ignored'), api('/api/status')]);
  const r2State = state.status.r2State || (state.status.r2Configured ? 'configured' : 'disconnected');
  const title = { verified: 'R2 verified', unavailable: 'R2 unavailable', configured: 'R2 configured', disconnected: 'Local only' }[r2State];
  document.querySelector('#connection-dot').classList.toggle('warn', r2State !== 'verified');
  document.querySelector('#connection-title').textContent = title;
  document.querySelector('#connection-detail').textContent = state.status.r2Configured ? state.status.r2.bucket : 'R2 not configured';
}

function gameSyncLabel(game) {
  if (!state.status.r2Configured) return 'R2 unavailable';
  if (!game.syncEnabled) return 'Sync off';
  return game.pendingSnapshotCount ? `${game.pendingSnapshotCount} pending` : 'Up to date';
}

function visibleGames() {
  const filters = state.gameFilters;
  const query = filters.query.trim().toLocaleLowerCase();
  return state.games.filter(game => {
    const matchesQuery = !query || [game.displayName, game.catalogName].some(value => String(value || '').toLocaleLowerCase().includes(query));
    const matchesWatcher = filters.watcher === 'all' || filters.watcher === 'watched' && game.enabled || filters.watcher === 'manual' && !game.enabled;
    const matchesSync = filters.sync === 'all' || filters.sync === 'pending' && game.pendingSnapshotCount > 0 || filters.sync === 'current' && game.pendingSnapshotCount === 0;
    const matchesSnapshots = filters.snapshots === 'all' || filters.snapshots === 'yes' && game.snapshotCount > 0 || filters.snapshots === 'no' && game.snapshotCount === 0;
    return matchesQuery && matchesWatcher && matchesSync && matchesSnapshots;
  });
}

function gamesPage() {
  setActiveNav('games');
  const games = visibleGames();
  root.innerHTML = `
    <header class="page-head">
      <div><p class="eyebrow">Your library</p><h1>Game saves</h1><p class="subtle">Immutable checkpoints, kept close and synced safely.</p></div>
      <div class="actions"><div class="sync-now-control"><button class="button" id="sync-all" title="Check watched games for changes, create snapshots, and upload pending snapshots" ${!state.status.r2Configured ? 'disabled' : ''}>${state.status.r2Configured ? 'Sync now' : 'Connect R2 to sync'}</button><small>${state.status.r2Configured ? `Last synced: ${escapeHTML(formatTime(state.status.r2LastSynced))}` : 'Last synced: Never'}</small></div><button class="button" id="scan-games">Scan for games</button><button class="button primary" id="add-game">+ Add game</button></div>
    </header>
    ${state.games.length ? `<section class="library-tools" aria-label="Filter games"><label class="search-field" for="game-search">Search games</label><input id="game-search" type="search" placeholder="Search display or catalog name" value="${escapeHTML(state.gameFilters.query)}"><label for="watcher-filter">Backup mode</label><select id="watcher-filter"><option value="all">All modes</option><option value="watched" ${state.gameFilters.watcher === 'watched' ? 'selected' : ''}>Watched</option><option value="manual" ${state.gameFilters.watcher === 'manual' ? 'selected' : ''}>Manual backups</option></select><label for="sync-filter">Sync state</label><select id="sync-filter"><option value="all">All sync states</option><option value="pending" ${state.gameFilters.sync === 'pending' ? 'selected' : ''}>Pending</option><option value="current" ${state.gameFilters.sync === 'current' ? 'selected' : ''}>Up to date</option></select><label for="snapshot-filter">Backups</label><select id="snapshot-filter"><option value="all">All backup states</option><option value="yes" ${state.gameFilters.snapshots === 'yes' ? 'selected' : ''}>Has snapshots</option><option value="no" ${state.gameFilters.snapshots === 'no' ? 'selected' : ''}>No snapshots</option></select></section>${games.length ? `<section class="games">${games.map(game => `
      <a class="game-card" href="#/games/${encodeURIComponent(game.id)}">
        <div class="cover">${cover(game)}<span class="badge"><span class="dot ${game.enabled ? '' : 'warn'}"></span>${game.enabled ? 'Watching' : 'Manual backups'}</span></div>
        <div class="card-body"><h2>${escapeHTML(game.displayName)}</h2><div class="card-meta"><span>${game.snapshotCount} snapshots</span><span>${escapeHTML(gameSyncLabel(game))}</span><span>${formatBytes(game.storedSize)}</span></div></div>
      </a>`).join('')}</section>` : '<section class="empty filtered-empty"><div><div class="empty-mark">⌕</div><h2>No games match these filters</h2><p class="subtle">Clear the filters to return to the full library.</p><button class="button" id="clear-game-filters">Clear filters</button></div></section>'}` : `
      <section class="empty"><div><div class="empty-mark">⌁</div><h2>No games tied in yet</h2><p class="subtle">Select Scan for games to check Steam, Epic, GOG, and existing local saves. You can also add any save folder yourself.</p><button class="button primary" id="empty-add">Add a custom game</button></div></section>`}`;
  document.querySelector('#add-game').addEventListener('click', showAddGame);
  document.querySelector('#empty-add')?.addEventListener('click', showAddGame);
  document.querySelector('#sync-all').addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true; button.textContent = 'Syncing…';
    try {
      const result = await api('/api/sync', { method: 'POST', body: '{}' }); await loadShared(); gamesPage();
      const failed = result.backupFailed + result.failed;
      const summary = `${result.checkedGames} games checked · ${result.createdSnapshots} snapshots created · ${result.synced} uploaded`;
      toast(failed || result.error ? `${summary} · ${failed} failed${result.error ? ` · ${result.error}` : ''}` : summary, Boolean(failed || result.error));
    }
    catch (error) { toast(error.message, true); button.disabled = false; button.textContent = 'Sync now'; }
  });
  const updateFilter = (name, value) => { state.gameFilters[name] = value; gamesPage(); };
  document.querySelector('#game-search')?.addEventListener('input', event => { const cursor = event.currentTarget.selectionStart; updateFilter('query', event.currentTarget.value); const input = document.querySelector('#game-search'); input.focus(); input.setSelectionRange(cursor, cursor); });
  document.querySelector('#watcher-filter')?.addEventListener('change', event => updateFilter('watcher', event.currentTarget.value));
  document.querySelector('#sync-filter')?.addEventListener('change', event => updateFilter('sync', event.currentTarget.value));
  document.querySelector('#snapshot-filter')?.addEventListener('change', event => updateFilter('snapshots', event.currentTarget.value));
  document.querySelector('#clear-game-filters')?.addEventListener('click', () => { state.gameFilters = { query: '', watcher: 'all', sync: 'all', snapshots: 'all' }; gamesPage(); });
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
  const canSync = state.status.r2Configured && game.syncEnabled && game.pendingSnapshotCount > 0;
  const syncLabel = !state.status.r2Configured ? 'Connect R2 to sync' : !game.syncEnabled ? 'Sync disabled' : game.pendingSnapshotCount ? `Sync pending (${game.pendingSnapshotCount})` : 'Up to date';
  root.innerHTML = `
    <a class="subtle" href="#/games">← All games</a>
    <section class="detail-head section">
      <div class="detail-cover">${cover(game, true)}</div>
      <div class="detail-copy">
        <p class="eyebrow">${escapeHTML(game.store)} ${game.storeId ? `· ${escapeHTML(game.storeId)}` : ''}</p>
        <h1>${escapeHTML(game.displayName)}</h1>
        <p class="subtle">${escapeHTML(game.notes || 'Watching for changes and preserving each distinct version.')}</p>
        <div class="actions"><button class="button primary" id="backup">Back up locally</button><button class="button" id="sync" ${canSync ? '' : 'disabled'}>${escapeHTML(syncLabel)}</button><button class="button" id="edit">Customize</button><button class="button danger" id="remove-game">${game.store === 'custom' ? 'Remove from library' : 'Ignore this game'}</button></div>
        <div class="stats"><div class="stat"><strong>${snapshots.length}</strong><small>Snapshots</small></div><div class="stat"><strong>${formatBytes(game.storedSize)}</strong><small>Stored</small></div><div class="stat"><strong>${formatTime(game.lastChange)}</strong><small>Last changed</small></div><div class="stat"><strong>${formatTime(game.lastBackup)}</strong><small>Last backup</small></div></div>
      </div>
    </section>
    <section class="section"><div class="section-title"><h2>Backups</h2><span class="subtle">Newest first</span></div>
      <div class="panel">${snapshots.length ? snapshots.map((snapshot, index) => `<div class="snapshot-entry"><div class="row"><div><strong>${formatTime(snapshot.createdAt)}</strong><small>${snapshot.files.length} files · ${formatBytes(snapshot.originalSize)} original · device ${escapeHTML(snapshot.deviceId.slice(-8))}</small></div><div class="actions"><span class="state ${snapshot.remoteState === 'synced' ? '' : 'local'}">${escapeHTML(snapshot.remoteState)}</span><button class="button small restore" data-snapshot="${escapeHTML(snapshot.id)}">Restore</button><button class="button small delete-snapshot" data-snapshot="${escapeHTML(snapshot.id)}" data-remote="${snapshot.remoteState === 'synced'}">Delete</button></div></div>${snapshotDetails(snapshot, snapshots[index + 1])}</div>`).join('') : '<div class="row"><div><strong>No snapshots yet</strong><small>Use Back up locally, or let the watcher catch the next save.</small></div></div>'}</div>
    </section>
    <section class="section"><div class="section-title"><h2>Save locations</h2><button class="button small" id="add-path">+ Add location</button></div>
      <div class="panel">${(paths || []).map(path => `<div class="row"><div><strong>${escapeHTML(path.resolved)}</strong><small>${escapeHTML(path.source)} · ${escapeHTML(path.template)}</small></div><div class="actions"><span class="state">${path.enabled ? 'Included' : 'Excluded'}</span><button class="button small toggle-path" data-path="${escapeHTML(path.id)}" data-enabled="${path.enabled}">${path.enabled ? 'Exclude' : 'Include'}</button>${path.source === 'custom' ? `<button class="button small remove-path" data-path="${escapeHTML(path.id)}">Remove</button>` : ''}</div></div>`).join('') || '<div class="row"><div><strong>No save locations configured</strong><small>Add a location to resume local backups.</small></div></div>'}</div>
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
  document.querySelector('#remove-game').addEventListener('click', () => showRemoveGame(game));
  document.querySelector('#add-path').addEventListener('click', () => showAddPath(game));
  document.querySelector('#add-exclusion').addEventListener('click', () => showAddExclusion(game));
  document.querySelectorAll('.toggle-path').forEach(button => button.addEventListener('click', async () => {
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/paths/${encodeURIComponent(button.dataset.path)}`, { method: 'PATCH', body: JSON.stringify({ enabled: button.dataset.enabled !== 'true' }) }); await gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  document.querySelectorAll('.remove-path').forEach(button => button.addEventListener('click', async () => {
    if (!confirm('Remove this custom save location? Existing snapshots are not deleted.')) return;
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/paths/${encodeURIComponent(button.dataset.path)}`, { method: 'DELETE' }); await gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  document.querySelectorAll('.remove-exclusion').forEach(button => button.addEventListener('click', async () => {
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/exclusions/${encodeURIComponent(button.dataset.exclusion)}`, { method: 'DELETE' }); await gamePage(id); }
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
  const automation = state.status.automation || {};
  const r2Health = state.status.r2State === 'verified'
    ? `Verified ${formatTime(state.status.r2LastVerified)}`
    : state.status.r2State === 'unavailable'
      ? `Unavailable since ${formatTime(state.status.r2LastFailure)}${state.status.r2LastError ? ` · ${state.status.r2LastError}` : ''}`
      : state.status.r2Configured ? 'Configured, but not yet verified by this version' : 'Not configured';
  root.innerHTML = `
    <header class="page-head"><div><p class="eyebrow">Make SaveKnot yours</p><h1>Settings</h1><p class="subtle">Control when backups move, how games are found, and where your data lives.</p></div></header>
    <section class="settings-grid">
      <form class="settings-card settings-card-wide" id="automation-form"><div class="settings-card-head"><div><p class="eyebrow">Background tasks</p><h2>Automatic sync & discovery</h2><p class="subtle">These schedules are independent. Turn on only the help you want.</p></div><span class="status-pill">Runs on this device</span></div>
        <div class="automation-grid">
          <div class="automation-option"><label class="switch-row"><span><strong>Sync watched games</strong><small>Upload pending snapshots only for games that are being watched.</small></span><input type="checkbox" name="periodicSyncEnabled" ${automation.periodicSyncEnabled ? 'checked' : ''}></label><label for="sync-interval">Check every</label><div class="input-suffix"><input id="sync-interval" type="number" min="1" max="10080" name="syncIntervalMinutes" value="${escapeHTML(automation.syncIntervalMinutes || 5)}"><span>minutes</span></div></div>
          <div class="automation-option"><label class="switch-row"><span><strong>Search for new save games</strong><small>Rescan Steam, Epic, GOG, and known local save locations.</small></span><input type="checkbox" name="periodicDiscoveryEnabled" ${automation.periodicDiscoveryEnabled ? 'checked' : ''}></label><label for="discovery-interval">Search every</label><div class="input-suffix"><input id="discovery-interval" type="number" min="5" max="43200" name="discoveryIntervalMinutes" value="${escapeHTML(automation.discoveryIntervalMinutes || 60)}"><span>minutes</span></div></div>
        </div><div class="form-actions"><span class="save-hint">Changes apply without restarting SaveKnot.</span><button class="button primary" type="submit">Save automation</button></div>
      </form>
      <form class="settings-card" id="r2-form"><h2>Cloudflare R2</h2><p class="subtle">Use a bucket-scoped token with Object Read & Write permission.</p>
        <div class="form-grid">
          <div class="field"><label for="account">Account ID</label><input id="account" name="accountId" required value="${escapeHTML(r2.accountId || '')}"></div>
          <div class="field"><label for="bucket">Bucket</label><input id="bucket" name="bucket" required value="${escapeHTML(r2.bucket || '')}"></div>
          <div class="field full"><label for="key">Access key ID</label><input id="key" name="accessKeyId" required autocomplete="off" value="${escapeHTML(r2.accessKeyId || '')}"></div>
          <div class="field full"><label for="secret">Secret access key</label><input id="secret" type="password" name="secretAccessKey" required autocomplete="new-password"><span class="help">Stored in your operating system credential vault, never in SQLite or config.json.</span></div>
          <div class="field full"><label for="prefix">Object prefix</label><input id="prefix" name="prefix" value="${escapeHTML(r2.prefix || 'saveknot')}"></div>
        </div><div class="callout r2-health"><strong>${escapeHTML(r2Health)}</strong><br>Configuration, recent verification, and current availability are reported separately.</div><div class="form-actions">${state.status.r2Configured ? '<button class="button danger" type="button" id="disconnect-r2">Disconnect R2</button>' : ''}<button class="button primary" type="submit">Test & connect</button></div>
      </form>
      <form class="settings-card" id="local-form"><h2>Device & local storage</h2><p class="subtle">Choose where local backups live and how SaveKnot behaves on this device.</p>
        <div class="form-grid">
          <div class="field full"><label for="local-backups">Local backup location</label><div class="actions"><input id="local-backups" name="localBackupDir" required value="${escapeHTML(state.status.localBackupDir || '')}"><button class="button browse" type="button" data-target="local-backups">Browse…</button></div></div>
          <div class="field full"><label class="switch-row compact"><span><strong>Launch when I sign in</strong><small>Keep automatic backups running without opening SaveKnot yourself.</small></span><input type="checkbox" name="launchAtLogin" ${state.status.launchAtLogin ? 'checked' : ''}></label></div>
          <div class="field"><label for="retention">Snapshots kept per game</label><input id="retention" type="number" min="1" max="10000" name="retentionKeep" value="${escapeHTML(state.status.retentionKeep || 50)}"></div>
          <details class="advanced-settings field full"><summary>Custom launcher locations</summary><p class="help">Only add these when a launcher is installed somewhere SaveKnot cannot detect.</p><div class="form-grid"><div class="field full"><label for="steam-roots">Extra Steam roots</label><textarea id="steam-roots" name="steamRoots" placeholder="One absolute folder per line">${escapeHTML((state.status.steamRoots || []).join('\n'))}</textarea></div><div class="field full"><label for="epic-manifests">Extra Epic manifest folders</label><textarea id="epic-manifests" name="epicManifests" placeholder="One absolute folder per line">${escapeHTML((state.status.epicManifests || []).join('\n'))}</textarea></div><div class="field full"><label for="gog-roots">Extra GOG game roots</label><textarea id="gog-roots" name="gogRoots" placeholder="One absolute folder per line">${escapeHTML((state.status.gogRoots || []).join('\n'))}</textarea></div></div></details>
        </div><div class="form-actions"><button class="button primary" type="submit">Save local settings</button></div>
      </form>
      <div class="settings-card"><div class="settings-card-head"><div><h2>Discovery diagnostics</h2><p class="subtle">Use these counts to diagnose an empty game list.</p></div><span class="status-pill">${diagnostics.updatedAt ? `Updated ${escapeHTML(formatTime(diagnostics.updatedAt))}` : 'Not cached yet'}</span></div>
        <div class="callout">Catalog: ${catalog.loaded ? `${catalog.gameCount || 0} games loaded` : `not loaded${catalog.lastError ? ` · ${escapeHTML(catalog.lastError)}` : ''}`}<br>Steam roots: ${(discovery.steamRoots || []).length ? discovery.steamRoots.map(escapeHTML).join(', ') : 'none detected'}<br>Installed: ${discovery.steamInstalled || 0} Steam · ${discovery.epicInstalled || 0} Epic · ${discovery.gogInstalled || 0} GOG<br>Matched in Ludusavi: ${discovery.catalogMatched || 0}<br>Found from local save data: ${discovery.localSaveGames || 0}${discovery.deepScanMillis ? ` (${discovery.deepScanMillis} ms deep scan)` : ''}<br>Registered: ${discovery.gamesRegistered || 0}${(discovery.unmatched || []).length ? `<br>Unmatched: ${discovery.unmatched.slice(0, 10).map(escapeHTML).join(', ')}` : ''}${discovery.lastError ? `<br>Error: ${escapeHTML(discovery.lastError)}` : ''}</div>
        <p class="help">Catalog checked: ${escapeHTML(formatTime(catalog.lastChecked))} · Last discovery scan: ${escapeHTML(formatTime(discovery.lastRun))}</p>
        <div class="form-actions"><button class="button" type="button" id="settings-scan">Scan now</button>${state.status.r2Configured ? '<button class="button" type="button" id="reconcile-r2">Refresh from R2</button>' : ''}</div>
      </div>
      <div class="settings-card"><h2>Ignored and removed games</h2><p class="subtle">These games stay out of the library and discovery scans. Snapshots and customizations are preserved.</p><div class="panel ignored-games">${state.ignoredGames.length ? state.ignoredGames.map(game => `<div class="row"><div><strong>${escapeHTML(game.displayName)}</strong><small>${game.store === 'custom' ? 'Removed custom game' : 'Ignored discovered game'} · ${game.snapshotCount} snapshots</small></div><button class="button small restore-game" data-game="${escapeHTML(game.id)}">Restore to library</button></div>`).join('') : '<div class="row"><div><strong>No ignored games</strong><small>Games removed from the library will appear here.</small></div></div>'}</div></div>
      <div class="settings-card"><h2>No middleman</h2><p class="subtle">There is no SaveKnot account, hosted API, telemetry collector, or central database.</p><div class="callout">The local daemon uploads content-addressed blobs first and publishes an immutable snapshot manifest last. An interrupted upload cannot create a valid partial backup.</div></div>
    </section>`;
  document.querySelector('#automation-form').addEventListener('submit', async event => {
    event.preventDefault(); const form = new FormData(event.currentTarget); const button = event.submitter; button.disabled = true;
    try {
      await api('/api/settings/automation', { method: 'PUT', body: JSON.stringify({ periodicSyncEnabled: form.get('periodicSyncEnabled') === 'on', syncIntervalMinutes: Number(form.get('syncIntervalMinutes')), periodicDiscoveryEnabled: form.get('periodicDiscoveryEnabled') === 'on', discoveryIntervalMinutes: Number(form.get('discoveryIntervalMinutes')) }) });
      await loadShared(); settingsPage(); toast('Automation schedule saved');
    } catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#r2-form').addEventListener('submit', async event => {
    event.preventDefault(); const button = event.submitter; button.disabled = true; button.textContent = 'Connecting…';
    try { const data = Object.fromEntries(new FormData(event.currentTarget)); await api('/api/r2', { method: 'POST', body: JSON.stringify(data) }); toast('R2 connection verified and saved'); await loadShared(); settingsPage(); }
    catch (error) { toast(error.message, true); button.disabled = false; button.textContent = 'Test & connect'; }
  });
  document.querySelector('#local-form').addEventListener('submit', async event => {
    event.preventDefault(); const form = new FormData(event.currentTarget); const button = event.submitter; button.disabled = true;
    const lines = name => String(form.get(name) || '').split(/\r?\n/).map(value => value.trim()).filter(Boolean);
    const operations = [
      ['snapshot retention', () => api('/api/settings/retention', { method: 'PUT', body: JSON.stringify({ keep: Number(form.get('retentionKeep')) }) })],
      ['storage and discovery paths', () => api('/api/settings/local', { method: 'PUT', body: JSON.stringify({ localBackupDir: form.get('localBackupDir'), steamRoots: lines('steamRoots'), epicManifests: lines('epicManifests'), gogRoots: lines('gogRoots') }) })],
      ['launch at login', () => api('/api/settings/autostart', { method: 'PUT', body: JSON.stringify({ enabled: form.get('launchAtLogin') === 'on' }) })],
    ];
    const failures = [];
    for (const [label, operation] of operations) {
      try { await operation(); } catch (error) { failures.push(`${label}: ${error.message}`); }
    }
    await loadShared(); settingsPage();
    if (failures.length) toast(`Some settings were not saved — ${failures.join('; ')}`, true);
    else toast('Local settings saved; select Scan now to rediscover games');
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
  document.querySelector('#disconnect-r2')?.addEventListener('click', async event => {
    if (!confirm('Disconnect R2 from this device? Local snapshots and objects already stored in R2 will not be deleted.')) return;
    event.currentTarget.disabled = true;
    try { await api('/api/r2', { method: 'DELETE' }); await loadShared(); settingsPage(); toast('R2 disconnected; local backups are unchanged'); }
    catch (error) { toast(error.message, true); event.currentTarget.disabled = false; }
  });
  document.querySelectorAll('.restore-game').forEach(button => button.addEventListener('click', async () => {
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(button.dataset.game)}/restore-library`, { method: 'POST', body: '{}' }); await loadShared(); settingsPage(); toast('Game restored to the library'); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  document.querySelector('#reconcile-r2')?.addEventListener('click', async event => {
    event.currentTarget.disabled = true;
    try { const result = await api('/api/r2/reconcile', { method: 'POST', body: '{}' }); await loadShared(); settingsPage(); toast(`Loaded ${result.snapshots} new snapshots; skipped ${result.skipped} already known`); }
    catch (error) { toast(error.message, true); event.currentTarget.disabled = false; }
  });
}

function activityPage() {
  setActiveNav('activity');
  root.innerHTML = `<header class="page-head"><div><p class="eyebrow">Live from this device</p><h1>Activity</h1><p class="subtle">Catalog, watcher, snapshot, restore, and upload events.</p></div></header><div class="panel" id="activity-list">${state.activity.length ? state.activity.map(activityRow).join('') : '<div class="row"><div><strong>Quiet for now</strong><small>New background events will appear here while this page is open.</small></div></div>'}</div>`;
  document.querySelectorAll('.retry-upload').forEach(button => button.addEventListener('click', async () => {
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(button.dataset.game)}/sync`, { method: 'POST', body: '{}' }); await loadShared(); activityPage(); toast('Pending snapshots synced'); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
}

function activityRow(event) {
  const game = [...state.games, ...state.ignoredGames].find(candidate => candidate.id === event.gameId);
  const gameName = game?.displayName || (event.gameId ? `Game ${event.gameId.slice(-8)}` : '');
  const retry = event.type === 'upload.failed' && event.gameId && state.status.r2Configured;
  return `<div class="row"><div><strong>${escapeHTML(event.type.replaceAll('.', ' · '))}${gameName ? ` · ${escapeHTML(gameName)}` : ''}</strong><small>${formatTime(event.timestamp)}${event.message ? ` · ${escapeHTML(event.message)}` : ''}</small></div>${event.gameId ? `<div class="actions">${retry ? `<button class="button small retry-upload" data-game="${escapeHTML(event.gameId)}">Retry sync for ${escapeHTML(gameName)}</button>` : ''}<a class="button small" href="#/games/${encodeURIComponent(event.gameId)}" aria-label="View ${escapeHTML(gameName)}">View ${escapeHTML(gameName)}</a></div>` : ''}</div>`;
}

let modalSequence = 0;
function modal(content) {
  const trigger = document.activeElement;
  const titleId = `modal-title-${++modalSequence}`;
  const backdrop = document.createElement('div'); backdrop.className = 'modal-backdrop'; backdrop.innerHTML = `<div class="modal" role="dialog" aria-modal="true" aria-labelledby="${titleId}"><div class="modal-head">${content}<button class="close" type="button" aria-label="Close dialog">×</button></div></div>`;
  const dialog = backdrop.querySelector('.modal');
  const heading = dialog.querySelector('h2');
  if (heading) heading.id = titleId;
  const close = () => { backdrop.remove(); trigger?.focus?.(); };
  backdrop.closeModal = close;
  backdrop.querySelector('.close').addEventListener('click', close);
  backdrop.addEventListener('click', event => { if (event.target === backdrop) close(); });
  backdrop.addEventListener('keydown', event => {
    if (event.key === 'Escape') { event.preventDefault(); close(); return; }
    if (event.key !== 'Tab') return;
    const focusable = [...dialog.querySelectorAll('button:not([disabled]), input:not([disabled]), textarea:not([disabled]), select:not([disabled]), a[href]')];
    if (!focusable.length) return;
    const first = focusable[0]; const last = focusable[focusable.length - 1];
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  });
  document.body.append(backdrop);
  setTimeout(() => (dialog.querySelector('[autofocus]') || dialog.querySelector('form input, form textarea, form select') || dialog.querySelector('button'))?.focus(), 0);
  return backdrop;
}

function showAddGame() {
  const element = modal(`<div><p class="eyebrow">Manual entry</p><h2>Add a game</h2><p class="subtle">Point SaveKnot at an absolute save folder or file.</p></div>`);
  const body = element.querySelector('.modal'); body.insertAdjacentHTML('beforeend', `<form id="game-form"><div class="form-grid"><div class="field full"><label for="new-game-name">Name</label><input id="new-game-name" name="name" required autofocus></div><div class="field full"><label for="new-game-path">Save location</label><div class="actions"><input id="new-game-path" name="path" required placeholder="Choose a save folder"><button class="button browse-path" type="button">Browse…</button></div><span class="help">Choose a folder or enter an absolute path. More locations can be added afterward.</span></div></div><div class="form-actions"><button class="button primary" type="submit">Add game</button></div></form>`);
  body.querySelector('.browse-path').addEventListener('click', async event => { event.currentTarget.disabled = true; try { await browseInto(body.querySelector('#new-game-path')); } catch (error) { toast(error.message, true); } finally { event.currentTarget.disabled = false; } });
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); const form = new FormData(event.currentTarget); try { const game = await api('/api/games', { method: 'POST', body: JSON.stringify({ name: form.get('name'), paths: [form.get('path')] }) }); element.closeModal(); await loadShared(); location.hash = `#/games/${game.id}`; } catch (error) { toast(error.message, true); } });
}

function showAddPath(game) {
  const element = modal(`<div><p class="eyebrow">${escapeHTML(game.displayName)}</p><h2>Add save location</h2></div>`); const body = element.querySelector('.modal');
  body.insertAdjacentHTML('beforeend', `<form><div class="field"><label for="added-save-path">Save folder</label><div class="actions"><input id="added-save-path" name="path" required autofocus><button class="button browse-path" type="button">Browse…</button></div></div><div class="form-actions"><button class="button primary" type="submit">Add location</button></div></form>`);
  body.querySelector('.browse-path').addEventListener('click', async event => { event.currentTarget.disabled = true; try { await browseInto(body.querySelector('input[name="path"]')); } catch (error) { toast(error.message, true); } finally { event.currentTarget.disabled = false; } });
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); try { await api(`/api/games/${encodeURIComponent(game.id)}/paths`, { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(event.currentTarget))) }); element.closeModal(); await gamePage(game.id); } catch (error) { toast(error.message, true); } });
}

function showAddExclusion(game) {
  const element = modal(`<div><p class="eyebrow">${escapeHTML(game.displayName)}</p><h2>Exclude files</h2><p class="subtle">Use a glob such as **/settings.json or **/*.tmp.</p></div>`); const body = element.querySelector('.modal');
  body.insertAdjacentHTML('beforeend', `<form><div class="field"><label for="exclusion-pattern">Glob pattern</label><input id="exclusion-pattern" name="pattern" required autofocus placeholder="**/*.tmp"></div><div class="form-actions"><button class="button primary" type="submit">Add exclusion</button></div></form>`);
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); try { await api(`/api/games/${encodeURIComponent(game.id)}/exclusions`, { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(event.currentTarget))) }); element.closeModal(); await gamePage(game.id); } catch (error) { toast(error.message, true); } });
}

function showEditGame(game, policy) {
  const element = modal(`<div><p class="eyebrow">Customize</p><h2>${escapeHTML(game.displayName)}</h2></div>`); const body = element.querySelector('.modal');
  const catalogHelp = game.store === 'custom' ? 'Links this manual entry to a Ludusavi title. Your custom save locations stay unchanged.' : 'Change this only when SaveKnot matched the wrong game.';
  body.insertAdjacentHTML('beforeend', `<form id="edit-form"><div class="form-grid"><div class="field full"><label for="edit-display-name">Display name</label><input id="edit-display-name" name="displayName" required value="${escapeHTML(game.displayName)}"></div><div class="field full"><label for="edit-catalog">Ludusavi catalog mapping</label><input id="edit-catalog" name="catalogId" list="catalog-options" value="${escapeHTML(game.catalogId || '')}" placeholder="Search catalog title"><datalist id="catalog-options"></datalist><span class="help">${escapeHTML(catalogHelp)}</span></div><div class="field full"><label for="edit-notes">Notes</label><textarea id="edit-notes" name="notes">${escapeHTML(game.notes || '')}</textarea></div><div class="field full"><label><input type="checkbox" name="enabled" ${game.enabled ? 'checked' : ''}> Watch for changes and create automatic local backups</label></div><div class="field full"><label><input type="checkbox" name="syncEnabled" ${game.syncEnabled ? 'checked' : ''}> Sync this game's pending snapshots to R2</label></div><div class="field"><label for="edit-quiet">Quiet debounce (seconds)</label><input id="edit-quiet" type="number" min="1" name="quietSeconds" value="${policy.quietSeconds}"></div><div class="field"><label for="edit-gap">Minimum snapshot gap (seconds)</label><input id="edit-gap" type="number" min="0" name="minGapSeconds" value="${policy.minGapSeconds}"></div><div class="field"><label for="edit-dirty">Maximum dirty duration (seconds)</label><input id="edit-dirty" type="number" min="1" name="maxDirtySeconds" value="${policy.maxDirtySeconds}"></div><div class="field full"><label for="edit-image">Custom picture</label><input id="edit-image" type="file" name="image" accept="image/png,image/jpeg,image/gif,image/webp">${String(game.image || '').startsWith('/artwork/') ? '<button class="button danger reset-image" type="button">Remove custom picture</button>' : ''}</div></div><div class="form-actions"><button class="button primary" type="submit">Save changes</button></div></form>`);
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
      element.closeModal(); await loadShared(); await gamePage(game.id);
    } catch (error) { toast(error.message, true); }
  });
  body.querySelector('.reset-image')?.addEventListener('click', async event => {
    event.currentTarget.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(game.id)}/image`, { method: 'DELETE' }); element.closeModal(); await loadShared(); await gamePage(game.id); toast('Custom picture removed'); }
    catch (error) { toast(error.message, true); event.currentTarget.disabled = false; }
  });
}

function showRemoveGame(game) {
  const action = game.store === 'custom' ? 'Remove from library' : 'Ignore this game';
  const element = modal(`<div><p class="eyebrow">Library management</p><h2>${escapeHTML(action)}</h2><p class="subtle">${escapeHTML(game.displayName)} will leave the active library and automatic watching will stop.</p></div>`);
  const body = element.querySelector('.modal');
  body.insertAdjacentHTML('beforeend', `<div class="callout">All local and R2 snapshots, save locations, exclusions, policies, and custom artwork will be preserved. You can restore this game later from Settings.</div><div class="form-actions"><button class="button" type="button" id="cancel-remove-game">Cancel</button><button class="button danger" type="button" id="confirm-remove-game">${escapeHTML(action)}</button></div>`);
  body.querySelector('#cancel-remove-game').addEventListener('click', element.closeModal);
  body.querySelector('#confirm-remove-game').addEventListener('click', async event => {
    event.currentTarget.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(game.id)}`, { method: 'DELETE' }); element.closeModal(); await loadShared(); location.hash = '#/games'; gamesPage(); toast(`${game.displayName} removed from the active library; snapshots preserved`); }
    catch (error) { toast(error.message, true); event.currentTarget.disabled = false; }
  });
}

async function route() {
  const parts = (location.hash || '#/games').slice(2).split('/');
  try {
    await loadShared();
    if (parts[0] === 'settings') settingsPage();
    else if (parts[0] === 'activity') activityPage();
    else if (parts[0] === 'games' && parts[1]) await gamePage(decodeURIComponent(parts[1]));
    else gamesPage();
    root.focus({ preventScroll: true });
  } catch (error) {
    if (error.status === 404 && parts[0] === 'games' && parts[1]) {
      root.innerHTML = '<section class="empty"><div><div class="empty-mark">?</div><h2>Game not found</h2><p class="subtle">This game may have been removed or ignored.</p><a class="button primary" href="#/games">Back to games</a></div></section>';
    } else {
      root.innerHTML = `<section class="empty"><div><div class="empty-mark">!</div><h2>SaveKnot could not load</h2><p class="subtle">${escapeHTML(error.message)}</p><button class="button" id="retry">Try again</button></div></section>`;
      document.querySelector('#retry').addEventListener('click', route);
    }
    root.focus({ preventScroll: true });
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
