import { escapeHTML, formatBytes, formatTime, snapshotChanges, parseRoute, syncProgressValues, pageItems, api, filterGames, activityText } from './helpers.mjs';

const state = {
  games: [], ignoredGames: [], status: null, activity: [],
  gameView: 'library', libraryPage: 0, activityOffset: 0, activityUntil: '',
  gameFilters: { query: '', watcher: 'all', sync: 'all', snapshots: 'all' },
  syncRequestActive: false,
  syncProgress: { active: false, phase: 'starting', completed: 0, total: 0 },
};
const root = document.querySelector('#app');
let pageVersion = 0;
let sharedRequest;
let detailVersion = 0;
let activeDialog;
const currentRoute = () => parseRoute(location.hash);
const onLibrary = () => currentRoute().name === 'games';

function pagination(page, pages, total, label = 'items') {
  if (pages <= 1) return '';
  return `<div class="pagination"><span class="subtle" role="status">${total} ${total === 1 ? label.replace(/s$/, '') : label} · Page ${page + 1} of ${pages}</span><div class="actions"><button class="button small" data-page="${page - 1}" ${page === 0 ? 'disabled' : ''}>Previous</button><button class="button small" data-page="${page + 1}" ${page + 1 >= pages ? 'disabled' : ''}>Next</button></div></div>`;
}

const countLabel = (count, singular, plural = `${singular}s`) => `${count} ${count === 1 ? singular : plural}`;

function showError(error) {
  if (error.name === 'AbortError') return;
  root.innerHTML = `<section class="empty"><div><div class="empty-mark">!</div><h1>${error.status === 404 ? 'Game not found' : 'SaveKnot could not load'}</h1><p class="subtle">${escapeHTML(error.message)}</p><div class="actions"><button class="button" id="retry">Try again</button><a class="button" href="#/games">Back to games</a></div></div></section>`;
  document.querySelector('#retry').addEventListener('click', route);
}

const initial = name => escapeHTML((name || '?').trim().charAt(0).toUpperCase());
const cover = game => game.image
  ? `<img loading="lazy" decoding="async" src="${escapeHTML(game.image)}" alt="">`
  : `<span class="cover-fallback" aria-hidden="true">${initial(game.displayName)}</span>`;

function snapshotDetails(snapshot, index) {
  return `<details class="snapshot-details" data-index="${index}"><summary>Inspect ${countLabel(snapshot.files.length, 'file')} and changes${snapshot.registry ? ' · registry included' : ''}</summary><div class="snapshot-files"></div></details>`;
}

function toast(message, error = false) {
  const element = document.createElement('div');
  element.className = `toast${error ? ' error' : ''}`;
  element.textContent = message;
  const region = document.querySelector('#toast-region');
  if (region.children.length >= 3) region.firstElementChild.remove();
  element.setAttribute('role', error ? 'alert' : 'status');
  region.append(element);
  setTimeout(() => element.remove(), 4500);
}

async function browseInto(input) {
  const selected = await api('/api/folder', { method: 'POST', body: '{}' });
  if (selected?.path) input.value = selected.path;
}

function setActiveNav(route) {
  document.querySelectorAll('nav a').forEach(link => { const active = link.dataset.route === route; link.classList.toggle('active', active); if (active) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current'); });
}

function loadShared() {
  if (sharedRequest) return sharedRequest;
  sharedRequest = Promise.all([api('/api/games'), api('/api/games/ignored'), api('/api/status')]).then(([games, ignored, status]) => {
    state.games = games || []; state.ignoredGames = ignored || []; state.status = status;
    const r2State = status.r2State || (status.r2Configured ? 'configured' : 'disconnected');
    document.querySelector('#connection-dot').classList.toggle('warn', r2State !== 'verified');
    document.querySelector('#connection-title').textContent = { verified: 'R2 verified', unavailable: 'R2 unavailable', configured: 'R2 configured', disconnected: 'Local only' }[r2State];
    document.querySelector('#connection-detail').textContent = status.r2Configured ? status.r2.bucket : 'Backups stay on this device';
  }).finally(() => { sharedRequest = null; });
  return sharedRequest;
}

document.addEventListener('error', event => {
  if (event.target instanceof HTMLImageElement) { const fallback = document.createElement('span'); fallback.className = 'cover-fallback'; fallback.textContent = '◇'; fallback.setAttribute('aria-hidden', 'true'); event.target.replaceWith(fallback); }
}, true);

function gameSyncLabel(game) {
  if (!state.status.r2Configured) {
    if (!game.syncEnabled) return 'Remote sync off';
    return `${game.pendingSnapshotCount ? `${game.pendingSnapshotCount} pending · ` : ''}Syncs after R2 is connected`;
  }
  if (!game.syncEnabled) return 'Sync off';
  return game.pendingSnapshotCount ? `${game.pendingSnapshotCount} pending` : 'Up to date';
}

function visibleGames() { return filterGames(state.games, state.gameFilters); }

function syncProgressLabel() {
  const progress = state.syncProgress;
  const values = syncProgressValues(progress);
  if (!progress.active && state.status?.syncInProgress) return 'Sync already running…';
  if (progress.phase === 'checking') return values ? `Checking games ${values.value}/${values.max}` : 'Checking watched games…';
  if (progress.phase === 'uploading') return values ? `Uploading snapshots ${values.value}/${values.max}` : 'Checking for pending uploads…';
  return 'Preparing sync…';
}

function updateSyncControl() {
  const control = document.querySelector('.sync-now-control');
  if (!control) return;
  const busy = state.syncRequestActive || state.syncProgress.active || Boolean(state.status?.syncInProgress);
  const connectedBusy = state.status.r2Configured && busy;
  const progress = state.syncProgress;
  const button = control.querySelector('#sync-all');
  button.disabled = connectedBusy;
  button.textContent = !state.status.r2Configured ? 'Connect R2 to sync' : busy ? 'Syncing…' : 'Sync now';
  button.title = state.status.r2Configured
    ? 'Check watched games for changes, create snapshots, and upload pending snapshots'
    : 'Open R2 settings. Pending snapshots will sync after R2 is connected.';
  button.setAttribute('aria-label', state.status.r2Configured
    ? busy ? 'Syncing watched games' : 'Sync watched games now'
    : 'Connect R2 to sync; pending snapshots will sync after R2 is connected');
  const progressArea = control.querySelector('.sync-progress-area');
  progressArea.hidden = !connectedBusy;
  progressArea.querySelector('small').textContent = syncProgressLabel();
  const bar = progressArea.querySelector('progress');
  const values = syncProgressValues(progress);
  if (progress.active && values) {
    bar.max = values.max;
    bar.value = values.value;
  } else {
    bar.removeAttribute('value');
  }
  control.querySelector('.last-sync').textContent = state.status.r2Configured
    ? `Last synced: ${formatTime(state.status.r2LastSynced)}`
    : 'Currently local only · Sync starts after R2 is connected';
}

function gamesPage() {
  if (!onLibrary()) return;
  acknowledgeUpdates();
  setActiveNav('games');
  const ignored = state.gameView === 'ignored';
  root.innerHTML = `
    <header class="page-head"><div><p class="eyebrow">Your library</p><h1>Game saves</h1><p class="subtle">${state.status.r2Configured ? 'Local checkpoints. Connected to R2.' : 'Your saves, backed up on this device.'}</p></div>
      <div class="actions"><button class="button" id="scan-games">Scan for games</button><button class="button primary" id="add-game">+ Add game</button></div></header>
    <section class="library-summary" aria-label="Library overview"><div><strong>${state.games.length}</strong><span>Games in library</span></div><div><strong>${state.games.reduce((sum, game) => sum + game.snapshotCount, 0)}</strong><span>Saved snapshots</span></div><div><strong>${state.games.filter(game => game.availableSources === 0).length}</strong><span>Need save locations</span></div><div class="sync-now-control"><button class="button" id="sync-all">Sync now</button><div class="sync-progress-area" hidden><progress aria-label="Sync progress"></progress><small></small></div><small class="last-sync"></small></div></section>
    <section class="library-tools" aria-label="Filter games"><div class="field"><label for="game-search">Search games</label><input id="game-search" type="search" placeholder="Name or catalog title" value="${escapeHTML(state.gameFilters.query)}"></div><div class="field"><label for="game-view">Game view</label><select id="game-view"><option value="library">Library</option><option value="ignored" ${ignored ? 'selected' : ''}>Ignored games (${state.ignoredGames.length})</option></select></div>${ignored ? '' : `<div class="field"><label for="watcher-filter">Backup mode</label><select id="watcher-filter"><option value="all">All modes</option><option value="watched" ${state.gameFilters.watcher === 'watched' ? 'selected' : ''}>Watched</option><option value="manual" ${state.gameFilters.watcher === 'manual' ? 'selected' : ''}>Manual backups</option></select></div><div class="field"><label for="sync-filter">Sync state</label><select id="sync-filter"><option value="all">All sync states</option><option value="pending" ${state.gameFilters.sync === 'pending' ? 'selected' : ''}>Pending</option><option value="current" ${state.gameFilters.sync === 'current' ? 'selected' : ''}>No pending uploads</option></select></div><div class="field"><label for="snapshot-filter">Backups</label><select id="snapshot-filter"><option value="all">All backup states</option><option value="yes" ${state.gameFilters.snapshots === 'yes' ? 'selected' : ''}>Has snapshots</option><option value="no" ${state.gameFilters.snapshots === 'no' ? 'selected' : ''}>No snapshots</option></select></div>`}</section>
    <div id="library-results"></div>`;
  const renderResults = () => {
    const ignored = state.gameView === 'ignored';
    const items = ignored ? state.ignoredGames.filter(game => [game.displayName, game.catalogName].some(name => String(name || '').toLocaleLowerCase().includes(state.gameFilters.query.trim().toLocaleLowerCase()))) : visibleGames();
    const page = pageItems(items, state.libraryPage, 36); state.libraryPage = page.page;
    const results = document.querySelector('#library-results');
    if (!results) return;
    results.innerHTML = page.items.length ? `${pagination(page.page, page.pages, page.total, 'games')}${ignored ? `<div class="panel">${page.items.map(game => `<div class="row"><div><strong>${escapeHTML(game.displayName)}</strong><small>${game.store === 'custom' ? 'Removed custom game' : 'Ignored discovered game'} · ${countLabel(game.snapshotCount, 'snapshot')} preserved</small></div><button class="button small restore-game" data-game="${escapeHTML(game.id)}">Restore to library</button></div>`).join('')}</div>` : `<section class="games">${page.items.map(game => `<a class="game-card ${game.availableSources === 0 ? 'no-backup-source' : ''}" href="#/games/${encodeURIComponent(game.id)}"><div class="cover">${cover(game)}<span class="badge"><span class="dot ${game.enabled ? '' : 'warn'}"></span>${game.enabled ? 'Watching' : 'Manual backups'}</span></div><div class="card-body"><h2 title="${escapeHTML(game.displayName)}">${escapeHTML(game.displayName)}</h2>${game.availableSources === 0 ? `<p class="source-warning">${game.sourceCount === 0 ? 'No save locations found' : 'No save files found'} · open to fix</p>` : ''}<div class="card-meta"><span>${countLabel(game.snapshotCount, 'snapshot')} · ${formatBytes(game.storedSize)}</span><span>${escapeHTML(gameSyncLabel(game))}</span></div></div></a>`).join('')}</section>`}` : `<section class="empty filtered-empty"><div><div class="empty-mark">◇</div><h2>${ignored ? 'No ignored games match' : state.games.length ? 'No games match these filters' : 'Add your first game'}</h2><p class="subtle">${ignored ? 'Removed games stay here with their snapshots and customizations.' : state.games.length ? 'Try another search or clear the filters.' : 'Scan for installed games, or choose a save folder to start backing it up.'}</p>${ignored || state.games.length ? '<button class="button" id="clear-game-filters">Clear filters</button>' : '<button class="button primary" id="empty-add">Add a custom game</button>'}</div></section>`;
    results.querySelectorAll('[data-page]').forEach(button => button.addEventListener('click', () => { state.libraryPage = Number(button.dataset.page); renderResults(); document.querySelector('#game-search').focus(); }));
    results.querySelector('#empty-add')?.addEventListener('click', showAddGame);
    results.querySelector('#clear-game-filters')?.addEventListener('click', () => { state.gameFilters = { query: '', watcher: 'all', sync: 'all', snapshots: 'all' }; state.libraryPage = 0; gamesPage(); document.querySelector('#game-search').focus(); });
    results.querySelectorAll('.restore-game').forEach(button => button.addEventListener('click', async () => {
      button.disabled = true; button.textContent = 'Restoring…';
      try { await api(`/api/games/${encodeURIComponent(button.dataset.game)}/restore-library`, { method: 'POST', body: '{}' }); await loadShared(); if (results.isConnected) { gamesPage(); toast('Game restored to the library'); } }
      catch (error) { toast(error.message, true); button.disabled = false; button.textContent = 'Restore to library'; }
    }));
  };
  renderResults(); updateSyncControl();
  document.querySelector('#add-game').addEventListener('click', showAddGame);
  document.querySelector('#game-view').addEventListener('change', event => { state.gameView = event.currentTarget.value; state.libraryPage = 0; gamesPage(); document.querySelector('#game-view').focus(); });
  document.querySelector('#game-search').addEventListener('input', event => { state.gameFilters.query = event.currentTarget.value; state.libraryPage = 0; renderResults(); });
  for (const [id, key] of [['watcher-filter', 'watcher'], ['sync-filter', 'sync'], ['snapshot-filter', 'snapshots']]) {
    document.getElementById(id)?.addEventListener('change', event => { state.gameFilters[key] = event.currentTarget.value; state.libraryPage = 0; renderResults(); });
  }
  document.querySelector('#sync-all').addEventListener('click', async () => {
    if (!state.status.r2Configured) { location.hash = '#/settings/r2'; return; }
    if (state.syncRequestActive || state.syncProgress.active || state.status.syncInProgress) return;
    state.syncRequestActive = true; state.syncProgress = { active: true, phase: 'starting', completed: 0, total: 0 }; updateSyncControl();
    try {
      const result = await api('/api/sync', { method: 'POST', body: '{}' });
      const failed = result.backupFailed + result.failed;
      toast(`${result.checkedGames} games checked · ${result.createdSnapshots} snapshots created · ${result.synced} uploaded${failed ? ` · ${failed} failed` : ''}${result.error ? ` · ${result.error}` : ''}`, Boolean(failed || result.error));
    } catch (error) { toast(error.message, true); }
    finally { state.syncRequestActive = false; state.syncProgress = { active: false, phase: 'starting', completed: 0, total: 0 }; await loadShared().catch(error => toast(error.message, true)); updateSyncControl(); if (onLibrary()) renderResults(); }
  });
  document.querySelector('#scan-games').addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true; button.textContent = 'Scanning…';
    try { const result = await api('/api/discovery', { method: 'POST', body: '{}' }); await loadShared(); if (button.isConnected) gamesPage(); toast(`Found ${result.discovery.steamInstalled} Steam installs; matched ${result.discovery.catalogMatched}`); }
    catch (error) { toast(error.message, true); }
    finally { button.disabled = false; button.textContent = 'Scan for games'; }
  });
}

async function gamePage(id, snapshotPage = 0) {
  if (currentRoute().name !== 'game' || currentRoute().id !== id) return;
  const version = ++detailVersion;
  const routeVersion = pageVersion;
  try {
  setActiveNav('games');
  root.innerHTML = '<p class="loading" role="status">Loading game…</p>';
  const [{ game, paths, registry, exclusions, policy }, snapshots] = await Promise.all([api(`/api/games/${encodeURIComponent(id)}`), api(`/api/games/${encodeURIComponent(id)}/snapshots`)]);
  if (version !== detailVersion || routeVersion !== pageVersion) return;
  const page = pageItems(snapshots, snapshotPage, 25);
  const snapshotOffset = page.page * 25;
  const canSync = state.status.r2Configured && game.syncEnabled && game.pendingSnapshotCount > 0;
  const syncLabel = !state.status.r2Configured ? 'Connect R2 to sync' : !game.syncEnabled ? 'Sync disabled' : game.pendingSnapshotCount ? `Sync pending (${game.pendingSnapshotCount})` : 'Up to date';
  const syncDisabled = state.status.r2Configured && !canSync;
  const syncDescription = game.syncEnabled
    ? `Currently local only. This game's pending snapshots will sync after R2 is connected.`
    : 'Currently local only. Remote sync will remain off after R2 is connected unless you enable it in Customize.';
  const comparison = snapshots.length > 1 ? `<div class="snapshot-compare" aria-label="Compare saved versions"><div class="field"><label for="compare-left">Compare version</label><select id="compare-left">${snapshots.map(snapshot => `<option value="${escapeHTML(snapshot.id)}">${formatTime(snapshot.createdAt)} · ${escapeHTML(snapshot.deviceId || 'unknown device')}</option>`).join('')}</select></div><span class="compare-with">with</span><div class="field"><label for="compare-right">Against version</label><select id="compare-right">${snapshots.map((snapshot, index) => `<option value="${escapeHTML(snapshot.id)}" ${index === 1 ? 'selected' : ''}>${formatTime(snapshot.createdAt)} · ${escapeHTML(snapshot.deviceId || 'unknown device')}</option>`).join('')}</select></div><button class="button small" id="compare-snapshots">Compare</button></div><div id="snapshot-comparison" class="snapshot-comparison" aria-live="polite"></div>` : '';
  acknowledgeUpdates();
  root.innerHTML = `
    <a class="subtle" href="#/games">← All games</a>
    <section class="detail-head section">
      <div class="detail-cover">${cover(game)}</div>
      <div class="detail-copy">
        <p class="eyebrow">${escapeHTML(game.store)} ${game.storeId ? `· ${escapeHTML(game.storeId)}` : ''}</p>
        <h1>${escapeHTML(game.displayName)}</h1>
        <p class="subtle">${escapeHTML(game.notes || 'Watching for changes and preserving each distinct version.')}</p>
        <div class="actions"><button class="button primary" id="backup" ${game.availableSources === 0 ? 'disabled' : ''}>Back up locally</button><button class="button" id="sync" ${syncDisabled ? 'disabled' : ''}${state.status.r2Configured ? '' : ' aria-describedby="game-sync-status" aria-label="Connect R2 to sync; this game is currently local only"'}>${escapeHTML(syncLabel)}</button><button class="button" id="edit">Customize</button><button class="button danger" id="remove-game">${game.store === 'custom' ? 'Remove from library' : 'Ignore this game'}</button></div>
        ${game.availableSources === 0 ? `<p class="source-warning" role="status">${game.sourceCount === 0 ? 'No save locations were found.' : 'The configured save locations contain no files.'} Add or check your save locations below before backing up.</p>` : ''}
        ${game.availableSources === 0 && snapshots.some(snapshot => snapshot.remoteState === 'synced') ? '<p class="help" role="status">This game has no save folder on this computer. Online backups are available below; use Download archive to save a portable copy.</p>' : ''}
        ${state.status.r2Configured ? '' : `<p class="help" id="game-sync-status">${escapeHTML(syncDescription)}</p>`}
        <div class="stats"><div class="stat"><strong>${snapshots.length}</strong><small>Snapshots</small></div><div class="stat"><strong>${formatBytes(game.storedSize)}</strong><small>Stored</small></div><div class="stat"><strong>${formatTime(game.lastChange)}</strong><small>Last changed</small></div><div class="stat"><strong>${formatTime(game.lastBackup)}</strong><small>Last backup</small></div></div>
      </div>
    </section>
    <section class="section"><div class="section-title"><h2>Backups</h2><span class="subtle">Newest first · other versions stay preserved</span></div>${comparison}${pagination(page.page, page.pages, page.total, 'snapshots')}
      <div class="panel">${snapshots.length ? page.items.map((snapshot, index) => `<div class="snapshot-entry"><div class="row"><div><strong>${formatTime(snapshot.createdAt)}${snapshot.active ? ' <span class="active-master">Active master</span>' : ''}</strong><small>${countLabel(snapshot.files.length, 'file')} · ${formatBytes(snapshot.originalSize)} · Device ${escapeHTML(snapshot.deviceId || 'unknown')} · Snapshot ID ${escapeHTML(snapshot.id.slice(0, 12))}</small></div><div class="actions"><span class="state ${snapshot.remoteState === 'synced' ? '' : 'local'}">${snapshot.remoteState === 'synced' ? 'Online backup' : 'On this device'}</span>${snapshot.remoteState === 'synced' ? `<a class="button small" href="/api/games/${encodeURIComponent(id)}/snapshots/${encodeURIComponent(snapshot.id)}/download" download>Download archive</a>` : ''}${snapshot.active ? '' : `<button class="button small set-active-snapshot" data-snapshot="${escapeHTML(snapshot.id)}">Make active master</button>`}<button class="button small restore" data-snapshot="${escapeHTML(snapshot.id)}">Restore</button><button class="button small delete-snapshot" data-snapshot="${escapeHTML(snapshot.id)}" data-remote="${snapshot.remoteState === 'synced'}">Delete</button></div></div>${snapshotDetails(snapshot, snapshotOffset + index)}</div>`).join('') : '<div class="row"><div><strong>No snapshots yet</strong><small>Use Back up locally, or let the watcher catch the next save.</small></div></div>'}</div>
    </section>
    <section class="section"><div class="section-title"><h2>Save locations</h2><button class="button small" id="add-path">+ Add location</button></div>
      <div class="panel">${(paths || []).map(path => `<div class="row"><div><strong>${escapeHTML(path.resolved)}</strong><small>${path.hasFiles ? 'Save files found' : 'No save files found'}</small></div><div class="actions"><span class="state">${path.enabled ? 'Included' : 'Excluded'}</span><button class="button small toggle-path" data-path="${escapeHTML(path.id)}" data-enabled="${path.enabled}">${path.enabled ? 'Exclude' : 'Include'}</button>${path.source === 'custom' ? `<button class="button small remove-path" data-path="${escapeHTML(path.id)}">Remove</button>` : ''}</div></div>`).join('') || '<div class="row"><div><strong>No save locations configured</strong><small>Add a location to resume local backups.</small></div></div>'}</div>
    </section>
    ${registry?.length ? `<section class="section"><div class="section-title"><h2>Windows registry saves</h2><span class="state">Included</span></div><div class="panel">${registry.map(item => `<div class="row"><div><strong>${escapeHTML(item.path)}</strong><small>Recursive registry backup and restore</small></div></div>`).join('')}</div></section>` : ''}
    <section class="section"><div class="section-title"><h2>File exclusions</h2><button class="button small" id="add-exclusion">+ Add exclusion</button></div><div class="panel">${exclusions?.length ? exclusions.map(item => `<div class="row"><div><strong>${escapeHTML(item.pattern)}</strong><small>Glob matched against relative and absolute save paths</small></div><button class="button small remove-exclusion" data-exclusion="${escapeHTML(item.id)}">Remove</button></div>`).join('') : '<div class="row"><div><strong>No exclusions</strong><small>Examples: **/settings.json or **/*.tmp</small></div></div>'}</div></section>`;
  document.querySelector('#backup').addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/backup`, { method: 'POST', body: '{}' }); toast('Snapshot created'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#sync').addEventListener('click', async event => {
    if (!state.status.r2Configured) { location.hash = '#/settings/r2'; return; }
    const button = event.currentTarget; button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/sync`, { method: 'POST', body: '{}' }); toast('Pending snapshots synced'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#edit').addEventListener('click', () => showEditGame(game, policy));
  document.querySelector('#remove-game').addEventListener('click', () => showRemoveGame(game));
  document.querySelector('#compare-snapshots')?.addEventListener('click', () => {
    const left = snapshots.find(snapshot => snapshot.id === document.querySelector('#compare-left').value);
    const right = snapshots.find(snapshot => snapshot.id === document.querySelector('#compare-right').value);
    const output = document.querySelector('#snapshot-comparison');
    if (!left || !right || left.id === right.id) { output.innerHTML = '<p class="help">Choose two different versions to compare.</p>'; return; }
    const changes = snapshotChanges(left, right);
    const registryChanged = (left.registry?.hash || '') !== (right.registry?.hash || '');
    output.innerHTML = `<div class="callout"><strong>${formatTime(left.createdAt)} · ${escapeHTML(left.deviceId || 'unknown device')}</strong> compared with <strong>${formatTime(right.createdAt)} · ${escapeHTML(right.deviceId || 'unknown device')}</strong><p class="help">File changes are shown below. ${registryChanged ? 'Windows registry data also differs between these versions.' : 'Windows registry data matches between these versions.'}</p><div class="file-list">${changes.map(file => `<div><span class="file-state ${file.state}">${escapeHTML(file.state)}</span><code title="${escapeHTML(file.path)}">${escapeHTML(file.path)}</code><small>${formatBytes(file.size)}</small></div>`).join('') || '<p class="help">These versions contain the same files.</p>'}</div></div>`;
  });
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
  document.querySelectorAll('.set-active-snapshot').forEach(button => button.addEventListener('click', async () => {
    const selected = snapshots.find(snapshot => snapshot.id === button.dataset.snapshot);
    if (!selected || !confirm(`Make the backup from ${formatTime(selected.createdAt)} on device ${selected.deviceId || 'unknown'} the active master for ${game.displayName}? This changes the preferred version across devices. The other snapshots stay preserved and can still be compared, downloaded, or restored.`)) return;
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/active-snapshot`, { method: 'PUT', body: JSON.stringify({ snapshotId: selected.id }) }); toast('Active master changed; other snapshots remain preserved'); await loadShared(); gamePage(id, snapshotPage); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  document.querySelectorAll('.delete-snapshot').forEach(button => button.addEventListener('click', async () => {
    const scope = button.dataset.remote === 'true' ? 'locally and from R2' : 'locally';
    if (!confirm(`Permanently delete this snapshot ${scope}? Shared content blobs are retained safely.`)) return;
    button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(id)}/snapshots/${encodeURIComponent(button.dataset.snapshot)}?remote=${button.dataset.remote}`, { method: 'DELETE' }); toast('Snapshot deleted'); await loadShared(); gamePage(id); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  }));
  root.querySelectorAll('[data-page]').forEach(button => button.addEventListener('click', () => gamePage(id, Number(button.dataset.page))));
  root.querySelectorAll('.snapshot-details').forEach(details => {
    let filePage = 0;
    const renderFiles = () => {
      const index = Number(details.dataset.index);
      const changes = snapshotChanges(snapshots[index], snapshots[index + 1]);
      const files = pageItems(changes, filePage, 100); filePage = files.page;
      details.querySelector('.snapshot-files').innerHTML = `${pagination(files.page, files.pages, files.total, 'files')}<div class="file-list">${files.items.map(file => `<div><span class="file-state ${file.state}">${escapeHTML(file.state)}</span><code title="${escapeHTML(file.path)}">${escapeHTML(file.path)}</code><small>${formatBytes(file.size)}</small></div>`).join('') || '<p class="help">No files; registry data only.</p>'}</div>`;
      details.querySelectorAll('[data-page]').forEach(button => button.addEventListener('click', () => { filePage = Number(button.dataset.page); renderFiles(); }));
    };
    details.addEventListener('toggle', () => { if (details.open) renderFiles(); else details.querySelector('.snapshot-files').replaceChildren(); });
  });
  } catch (error) { if (version === detailVersion && routeVersion === pageVersion) showError(error); }
}

function settingsPage(preserveDrafts = false) {
  if (currentRoute().name !== 'settings') return;
  const drafts = preserveDrafts ? [...root.querySelectorAll('form input, form textarea, form select')].map(input => ({ id: input.id, name: input.name, form: input.form.id, value: input.value, checked: input.checked })) : [];
  const expanded = root.querySelector('.advanced-settings')?.open;
  const focusID = document.activeElement?.id;
  acknowledgeUpdates();
  setActiveNav('settings');
  const r2 = state.status.r2 || {};
  const diagnostics = state.status.diagnostics || {};
  const catalog = diagnostics.catalog || {};
  const discovery = diagnostics.discovery || {};
  const automation = state.status.automation || {};
  const automationSyncHelp = state.status.r2Configured
    ? 'Check watched games for changes, create local snapshots, then upload pending snapshots.'
    : 'Currently local only. This preference is saved; watched games will sync after R2 is connected.';
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
          <div class="automation-option"><label class="switch-row"><span><strong>Sync watched games when R2 is connected</strong><small>${escapeHTML(automationSyncHelp)}</small></span><input type="checkbox" name="periodicSyncEnabled" ${automation.periodicSyncEnabled ? 'checked' : ''}></label><label for="sync-interval">Check every</label><div class="input-suffix"><input id="sync-interval" type="number" min="1" max="10080" name="syncIntervalMinutes" value="${escapeHTML(automation.syncIntervalMinutes || 5)}"><span>minutes</span></div></div>
          <div class="automation-option"><label class="switch-row"><span><strong>Search for new save games</strong><small>Rescan Steam, Epic, GOG, and known local save locations.</small></span><input type="checkbox" name="periodicDiscoveryEnabled" ${automation.periodicDiscoveryEnabled ? 'checked' : ''}></label><label for="discovery-interval">Search every</label><div class="input-suffix"><input id="discovery-interval" type="number" min="5" max="43200" name="discoveryIntervalMinutes" value="${escapeHTML(automation.discoveryIntervalMinutes || 60)}"><span>minutes</span></div></div>
        </div><div class="form-actions"><span class="save-hint">Changes apply without restarting SaveKnot.</span><button class="button primary" type="submit">Save automation</button></div>
      </form>
      <form class="settings-card" id="r2-form" tabindex="-1" aria-labelledby="r2-heading" aria-describedby="r2-help r2-health"><h2 id="r2-heading">Cloudflare R2</h2><p class="subtle" id="r2-help">Connect R2 to start remote sync. Until then, snapshots remain local and saved sync preferences are deferred.</p>
        <div class="form-grid">
          <div class="field"><label for="account">Account ID</label><input id="account" name="accountId" required value="${escapeHTML(r2.accountId || '')}"></div>
          <div class="field"><label for="bucket">Bucket</label><input id="bucket" name="bucket" required value="${escapeHTML(r2.bucket || '')}"></div>
          <div class="field full"><label for="key">Access key ID</label><input id="key" name="accessKeyId" required autocomplete="off" value="${escapeHTML(r2.accessKeyId || '')}"></div>
          <div class="field full"><label for="secret">Secret access key</label><input id="secret" type="password" name="secretAccessKey" required autocomplete="new-password"><span class="help">Stored in your operating system credential vault, never in SQLite or config.json.</span></div>
          <div class="field full"><label for="prefix">Object prefix</label><input id="prefix" name="prefix" value="${escapeHTML(r2.prefix || 'saveknot')}"></div>
        </div><div class="callout r2-health" id="r2-health"><strong>${escapeHTML(r2Health)}</strong></div><div class="form-actions">${state.status.r2Configured ? '<button class="button" type="button" id="reconcile-r2">Refresh from R2</button><button class="button danger" type="button" id="disconnect-r2">Disconnect R2</button>' : ''}<button class="button primary" type="submit">Test & connect</button></div>
      </form>
      <form class="settings-card" id="local-form"><h2>Device & local storage</h2><p class="subtle">Choose where local backups live and how SaveKnot behaves on this device.</p>
        <div class="form-grid">
          <div class="field full"><label for="local-backups">Local backup location</label><div class="actions"><input id="local-backups" name="localBackupDir" required value="${escapeHTML(state.status.localBackupDir || '')}"><button class="button browse" type="button" data-target="local-backups">Browse…</button></div></div>
          <div class="field full"><label class="switch-row compact"><span><strong>Launch when I sign in</strong><small>Keep automatic backups running without opening SaveKnot yourself.</small></span><input type="checkbox" name="launchAtLogin" ${state.status.launchAtLogin ? 'checked' : ''}></label></div>
          <div class="field"><label for="retention">Snapshots kept per game</label><input id="retention" type="number" min="1" max="10000" name="retentionKeep" value="${escapeHTML(state.status.retentionKeep || 50)}"></div>
          <details class="advanced-settings field full"><summary>Custom launcher locations</summary><p class="help">Only add these when a launcher is installed somewhere SaveKnot cannot detect.</p><div class="form-grid"><div class="field full"><label for="steam-roots">Extra Steam roots</label><textarea id="steam-roots" name="steamRoots" placeholder="One absolute folder per line">${escapeHTML((state.status.steamRoots || []).join('\n'))}</textarea></div><div class="field full"><label for="epic-manifests">Extra Epic manifest folders</label><textarea id="epic-manifests" name="epicManifests" placeholder="One absolute folder per line">${escapeHTML((state.status.epicManifests || []).join('\n'))}</textarea></div><div class="field full"><label for="gog-roots">Extra GOG game roots</label><textarea id="gog-roots" name="gogRoots" placeholder="One absolute folder per line">${escapeHTML((state.status.gogRoots || []).join('\n'))}</textarea></div></div></details>
        </div><div class="form-actions"><button class="button primary" type="submit">Save local settings</button></div>
      </form>
      <div class="settings-card"><div class="settings-card-head"><div><h2>Find games</h2><p class="subtle">Scan launchers and known save locations when a game is missing.</p></div></div>
        <details class="advanced-settings diagnostics-details"><summary>Troubleshooting details</summary><div class="callout">Catalog: ${catalog.loaded ? `${catalog.gameCount || 0} games ready` : `not ready${catalog.lastError ? ` · ${escapeHTML(catalog.lastError)}` : ''}`}<br>Installed games found: ${(discovery.steamInstalled || 0) + (discovery.epicInstalled || 0) + (discovery.gogInstalled || 0)}<br>Matched save definitions: ${discovery.catalogMatched || 0}<br>Found from local save data: ${discovery.localSaveGames || 0}<br>Added to library: ${discovery.gamesRegistered || 0}${(discovery.unmatched || []).length ? `<br>Not matched: ${discovery.unmatched.slice(0, 10).map(escapeHTML).join(', ')}` : ''}${discovery.lastError ? `<br>Error: ${escapeHTML(discovery.lastError)}` : ''}</div><p class="help">Last scan: ${escapeHTML(formatTime(discovery.lastRun))}</p></details>
        <div class="form-actions"><button class="button primary" type="button" id="settings-scan">Scan now</button></div>
      </div>
    </section>`;
  document.querySelector('#automation-form').addEventListener('submit', async event => {
    event.preventDefault(); const form = new FormData(event.currentTarget); const button = event.submitter || event.currentTarget.querySelector('[type="submit"]'); button.disabled = true;
    try {
      await api('/api/settings/automation', { method: 'PUT', body: JSON.stringify({ periodicSyncEnabled: form.get('periodicSyncEnabled') === 'on', syncIntervalMinutes: Number(form.get('syncIntervalMinutes')), periodicDiscoveryEnabled: form.get('periodicDiscoveryEnabled') === 'on', discoveryIntervalMinutes: Number(form.get('discoveryIntervalMinutes')) }) });
      await loadShared(); button.disabled = false; toast('Automation schedule saved');
    } catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#r2-form').addEventListener('submit', async event => {
    event.preventDefault(); const button = event.submitter || event.currentTarget.querySelector('[type="submit"]'); button.disabled = true; button.textContent = 'Connecting…';
    try { const data = Object.fromEntries(new FormData(event.currentTarget)); await api('/api/r2', { method: 'POST', body: JSON.stringify(data) }); toast('R2 connection verified and saved'); await loadShared(); if (button.isConnected) { document.querySelector('#secret').value = ''; settingsPage(true); } }
    catch (error) { toast(error.message, true); button.disabled = false; button.textContent = 'Test & connect'; }
  });
  document.querySelector('#local-form').addEventListener('submit', async event => {
    event.preventDefault(); const form = new FormData(event.currentTarget); const button = event.submitter || event.currentTarget.querySelector('[type="submit"]'); button.disabled = true;
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
    try { await loadShared(); } catch (error) { failures.push(`refresh: ${error.message}`); }
    button.disabled = false;
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
    const button = event.currentTarget; button.disabled = true;
    try { await api('/api/discovery', { method: 'POST', body: '{}' }); await loadShared(); if (button.isConnected) settingsPage(true); toast('Discovery scan completed'); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#disconnect-r2')?.addEventListener('click', async event => {
    if (!confirm('Disconnect R2 from this device? Local snapshots and objects already stored in R2 will not be deleted.')) return;
    const button = event.currentTarget; button.disabled = true;
    try { await api('/api/r2', { method: 'DELETE' }); await loadShared(); if (button.isConnected) settingsPage(true); toast('R2 disconnected; local backups are unchanged'); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  document.querySelector('#reconcile-r2')?.addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true;
    try { const result = await api('/api/r2/reconcile', { method: 'POST', body: '{}' }); await loadShared(); if (button.isConnected) settingsPage(true); toast(`Loaded ${result.snapshots} new snapshots; skipped ${result.skipped} already known`); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
  for (const draft of drafts) {
    const input = document.getElementById(draft.form)?.elements.namedItem(draft.name);
    if (input) { input.value = draft.value; if (input.type === 'checkbox') input.checked = draft.checked; }
  }
  if (expanded) document.querySelector('.advanced-settings').open = true;
  if (preserveDrafts && focusID) document.getElementById(focusID)?.focus({ preventScroll: true });

}

async function activityPage() {
  if (currentRoute().name !== 'activity') return;
  setActiveNav('activity');
  const version = pageVersion;
  const request = ++activityRequest;
  try {
    const result = await api(`/api/activity?offset=${state.activityOffset}${state.activityUntil ? `&until=${encodeURIComponent(state.activityUntil)}` : ''}`);
    if (version !== pageVersion || request !== activityRequest) return;
    state.activity = result.events; state.activityUntil = result.until;
    acknowledgeUpdates();
    root.innerHTML = `<header class="page-head"><div><p class="eyebrow">History on this device</p><h1>Activity</h1><p class="subtle">Events from the last 30 days, including previous sessions.</p></div><button class="button" id="refresh-activity">${activityDirty ? 'Show new activity' : 'Refresh activity'}</button></header>${pagination(Math.floor(state.activityOffset / 50), Math.max(1, Math.ceil(result.total / 50)), result.total, 'events')}<div class="panel" id="activity-list">${state.activity.length ? state.activity.map(activityRow).join('') : '<div class="row"><div><strong>Quiet for now</strong><small>Backup, restore, discovery and sync events will appear here.</small></div></div>'}</div>`;
    document.querySelector('#refresh-activity').addEventListener('click', () => { state.activityOffset = 0; state.activityUntil = ''; activityDirty = false; activityPage(); });
    root.querySelectorAll('[data-page]').forEach(button => button.addEventListener('click', () => { state.activityOffset = Number(button.dataset.page) * 50; activityPage(); }));
    document.querySelectorAll('.retry-upload').forEach(button => button.addEventListener('click', async () => {
      button.disabled = true;
      try { await api(`/api/games/${encodeURIComponent(button.dataset.game)}/sync`, { method: 'POST', body: '{}' }); await loadShared(); toast('Pending snapshots synced'); }
      catch (error) { toast(error.message, true); }
      finally { button.disabled = false; }
    }));
  } catch (error) { if (version === pageVersion && request === activityRequest) showError(error); }
}

function activityRow(event) {
  const game = [...state.games, ...state.ignoredGames].find(candidate => candidate.id === event.gameId);
  const gameName = game?.displayName || (event.gameId ? `Game ${event.gameId.slice(-8)}` : '');
  const retry = event.type === 'upload.failed' && event.gameId && state.status.r2Configured;
  const [title, detail] = activityText(event, gameName);
  return `<div class="row"><div><strong>${escapeHTML(title)}</strong><small>${formatTime(event.timestamp)}${detail ? ` · ${escapeHTML(detail)}` : ''}</small></div>${event.gameId ? `<div class="actions">${retry ? `<button class="button small retry-upload" data-game="${escapeHTML(event.gameId)}">Retry upload</button>` : ''}<a class="button small" href="#/games/${encodeURIComponent(event.gameId)}" aria-label="Open ${escapeHTML(gameName)}">Open game</a></div>` : ''}</div>`;
}

let modalSequence = 0;
function modal(content) {
  activeDialog?.closeModal();
  const trigger = document.activeElement;
  const element = document.createElement('dialog');
  element.className = 'modal';
  element.innerHTML = `<div class="modal-head">${content}<button class="close" type="button" aria-label="Close dialog">×</button></div>`;
  const heading = element.querySelector('h2');
  if (heading) { heading.id = `modal-title-${++modalSequence}`; element.setAttribute('aria-labelledby', heading.id); }
  element.classList.add('native-dialog');
  element.closeModal = () => element.close();
  element.addEventListener('close', () => { element.dispatchEvent(new Event('cleanup')); element.remove(); if (activeDialog === element) activeDialog = null; if (trigger?.isConnected) trigger.focus(); }, { once: true });
  element.querySelector('.close').addEventListener('click', element.closeModal);
  element.addEventListener('click', event => { if (event.target === element) { const rect = element.getBoundingClientRect(); if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) element.close(); } });
  document.body.append(element); activeDialog = element;
  queueMicrotask(() => { if (element.isConnected) { element.showModal(); (element.querySelector('[autofocus], form input, button'))?.focus(); } });
  return element;
}

function showAddGame() {
  const element = modal(`<div><p class="eyebrow">Manual entry</p><h2>Add a game</h2><p class="subtle">Point SaveKnot at an absolute save folder or file.</p></div>`);
  const body = element; body.insertAdjacentHTML('beforeend', `<form id="game-form"><div class="form-grid"><div class="field full"><label for="new-game-name">Name</label><input id="new-game-name" name="name" required autofocus></div><div class="field full"><label for="new-game-path">Save location</label><div class="actions"><input id="new-game-path" name="path" required placeholder="Choose a save folder"><button class="button browse-path" type="button">Browse…</button></div><span class="help">Choose a folder or enter an absolute path. More locations can be added afterward.</span></div></div><div class="form-actions"><button class="button primary" type="submit">Add game</button></div></form>`);
  body.querySelector('.browse-path').addEventListener('click', async event => { const button = event.currentTarget; button.disabled = true; try { await browseInto(body.querySelector('#new-game-path')); } catch (error) { toast(error.message, true); } finally { button.disabled = false; } });
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); const button = event.submitter || event.currentTarget.querySelector('[type="submit"]'); if (button.disabled) return; button.disabled = true; const form = new FormData(event.currentTarget); try { const game = await api('/api/games', { method: 'POST', body: JSON.stringify({ name: form.get('name'), paths: [form.get('path')] }) }); element.closeModal(); await loadShared(); location.hash = `#/games/${encodeURIComponent(game.id)}`; } catch (error) { toast(error.message, true); } finally { button.disabled = false; } });
}

function showAddPath(game) {
  const element = modal(`<div><p class="eyebrow">${escapeHTML(game.displayName)}</p><h2>Add save location</h2></div>`); const body = element;
  body.insertAdjacentHTML('beforeend', `<form><div class="field"><label for="added-save-path">Save folder</label><div class="actions"><input id="added-save-path" name="path" required autofocus><button class="button browse-path" type="button">Browse…</button></div></div><div class="form-actions"><button class="button primary" type="submit">Add location</button></div></form>`);
  body.querySelector('.browse-path').addEventListener('click', async event => { const button = event.currentTarget; button.disabled = true; try { await browseInto(body.querySelector('input[name="path"]')); } catch (error) { toast(error.message, true); } finally { button.disabled = false; } });
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); const button = event.submitter || event.currentTarget.querySelector('[type="submit"]'); if (button.disabled) return; button.disabled = true; try { await api(`/api/games/${encodeURIComponent(game.id)}/paths`, { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(event.currentTarget))) }); element.closeModal(); await gamePage(game.id); } catch (error) { toast(error.message, true); } finally { button.disabled = false; } });
}

function showAddExclusion(game) {
  const element = modal(`<div><p class="eyebrow">${escapeHTML(game.displayName)}</p><h2>Exclude files</h2><p class="subtle">Use a glob such as **/settings.json or **/*.tmp.</p></div>`); const body = element;
  body.insertAdjacentHTML('beforeend', `<form><div class="field"><label for="exclusion-pattern">Glob pattern</label><input id="exclusion-pattern" name="pattern" required autofocus placeholder="**/*.tmp"></div><div class="form-actions"><button class="button primary" type="submit">Add exclusion</button></div></form>`);
  body.querySelector('form').addEventListener('submit', async event => { event.preventDefault(); const button = event.submitter || event.currentTarget.querySelector('[type="submit"]'); if (button.disabled) return; button.disabled = true; try { await api(`/api/games/${encodeURIComponent(game.id)}/exclusions`, { method: 'POST', body: JSON.stringify(Object.fromEntries(new FormData(event.currentTarget))) }); element.closeModal(); await gamePage(game.id); } catch (error) { toast(error.message, true); } finally { button.disabled = false; } });
}

function showEditGame(game, policy) {
  const element = modal(`<div><p class="eyebrow">Customize</p><h2>${escapeHTML(game.displayName)}</h2></div>`); const body = element;
  const catalogHelp = game.store === 'custom' ? 'Links this manual entry to a Ludusavi title. Your custom save locations stay unchanged.' : 'Change this only when SaveKnot matched the wrong game.';
  body.insertAdjacentHTML('beforeend', `<form id="edit-form"><div class="form-grid"><div class="field full"><label for="edit-display-name">Display name</label><input id="edit-display-name" name="displayName" required value="${escapeHTML(game.displayName)}"></div><div class="field full"><label for="edit-notes">Notes</label><textarea id="edit-notes" name="notes">${escapeHTML(game.notes || '')}</textarea></div><div class="field full"><label><input type="checkbox" name="enabled" ${game.enabled ? 'checked' : ''}> Watch for changes and create automatic local backups</label></div><div class="field full"><label><input type="checkbox" name="syncEnabled" ${game.syncEnabled ? 'checked' : ''}> Sync this game's pending snapshots when R2 is connected</label>${state.status.r2Configured ? '' : '<span class="help">Currently local only. This preference will take effect after R2 is connected.</span>'}</div><div class="field full"><label for="edit-image">Custom picture</label><input id="edit-image" type="file" name="image" accept="image/png,image/jpeg,image/gif,image/webp">${String(game.image || '').startsWith('/artwork/') ? '<button class="button danger reset-image" type="button">Remove custom picture</button>' : ''}</div><details class="advanced-settings field full"><summary>Advanced backup settings</summary><p class="help">Change these only to correct a catalog match or fine-tune automatic backup timing.</p><div class="form-grid"><div class="field full"><label for="edit-catalog">Game catalog match</label><input id="edit-catalog" name="catalogId" list="catalog-options" value="${escapeHTML(game.catalogId || '')}" placeholder="Search catalog title"><datalist id="catalog-options"></datalist><span class="help">${escapeHTML(catalogHelp)}</span></div><div class="field"><label for="edit-quiet">Wait after changes (seconds)</label><input id="edit-quiet" type="number" min="1" name="quietSeconds" value="${policy.quietSeconds}"></div><div class="field"><label for="edit-gap">Minimum time between backups (seconds)</label><input id="edit-gap" type="number" min="0" name="minGapSeconds" value="${policy.minGapSeconds}"></div><div class="field"><label for="edit-dirty">Maximum wait (seconds)</label><input id="edit-dirty" type="number" min="1" name="maxDirtySeconds" value="${policy.maxDirtySeconds}"></div></div></details></div><div class="form-actions"><button class="button primary" type="submit">Save changes</button></div></form>`);
  const catalogInput = body.querySelector('input[name="catalogId"]'); let catalogTimer; let catalogRequest;
  element.addEventListener('cleanup', () => { clearTimeout(catalogTimer); catalogRequest?.abort(); });
  const suggestCatalog = () => { clearTimeout(catalogTimer); catalogRequest?.abort(); catalogRequest = new AbortController(); catalogTimer = setTimeout(async () => { try { const choices = await api(`/api/catalog?q=${encodeURIComponent(catalogInput.value)}`, { signal: catalogRequest.signal }); body.querySelector('#catalog-options').innerHTML = choices.map(choice => `<option value="${escapeHTML(choice.id)}"></option>`).join(''); } catch { /* Suggestions are optional; submit still validates the mapping. */ } }, 200); };
  catalogInput.addEventListener('input', suggestCatalog); suggestCatalog();
  body.querySelector('form').addEventListener('submit', async event => {
    event.preventDefault(); const button = event.submitter || event.currentTarget.querySelector('[type="submit"]'); if (button.disabled) return; button.disabled = true; const form = new FormData(event.currentTarget);
    try {
      await api(`/api/games/${encodeURIComponent(game.id)}`, { method: 'PATCH', body: JSON.stringify({ displayName: form.get('displayName'), notes: form.get('notes'), enabled: form.get('enabled') === 'on', syncEnabled: form.get('syncEnabled') === 'on' }) });
      if (form.get('catalogId') && form.get('catalogId') !== game.catalogId) await api(`/api/games/${encodeURIComponent(game.id)}/remap`, { method: 'PUT', body: JSON.stringify({ catalogId: form.get('catalogId') }) });
      await api(`/api/games/${encodeURIComponent(game.id)}/policy`, { method: 'PUT', body: JSON.stringify({ quietSeconds: Number(form.get('quietSeconds')), minGapSeconds: Number(form.get('minGapSeconds')), maxDirtySeconds: Number(form.get('maxDirtySeconds')) }) });
      const image = form.get('image'); if (image?.size) { const upload = new FormData(); upload.set('image', image); await api(`/api/games/${encodeURIComponent(game.id)}/image`, { method: 'POST', body: upload }); }
      element.closeModal(); await loadShared(); await gamePage(game.id);
    } catch (error) { toast(error.message, true); } finally { button.disabled = false; }
  });
  body.querySelector('.reset-image')?.addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(game.id)}/image`, { method: 'DELETE' }); element.closeModal(); await loadShared(); await gamePage(game.id); toast('Custom picture removed'); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
}

function showRemoveGame(game) {
  const action = game.store === 'custom' ? 'Remove from library' : 'Ignore this game';
  const element = modal(`<div><p class="eyebrow">Library management</p><h2>${escapeHTML(action)}</h2><p class="subtle">${escapeHTML(game.displayName)} will leave the active library and automatic watching will stop.</p></div>`);
  const body = element;
  body.insertAdjacentHTML('beforeend', `<div class="callout">All local and R2 snapshots, save locations, exclusions, policies, and custom artwork will be preserved. You can restore this game from the Ignored games view in Games.</div><div class="form-actions"><button class="button" type="button" id="cancel-remove-game">Cancel</button><button class="button danger" type="button" id="confirm-remove-game">${escapeHTML(action)}</button></div>`);
  body.querySelector('#cancel-remove-game').addEventListener('click', element.closeModal);
  body.querySelector('#confirm-remove-game').addEventListener('click', async event => {
    const button = event.currentTarget; button.disabled = true;
    try { await api(`/api/games/${encodeURIComponent(game.id)}`, { method: 'DELETE' }); element.closeModal(); await loadShared(); location.hash = '#/games'; gamesPage(); toast(`${game.displayName} removed from the active library; snapshots preserved`); }
    catch (error) { toast(error.message, true); button.disabled = false; }
  });
}

async function route() {
  const version = ++pageVersion;
  ++detailVersion;
  activeDialog?.closeModal();
  const target = currentRoute();
  state.activity = [];
  root.innerHTML = '<p class="loading" role="status">Loading SaveKnot…</p>';
  try {
    await loadShared();
    if (version !== pageVersion) return;
    if (target.name === 'settings') settingsPage();
    else if (target.name === 'activity') { state.activityOffset = 0; state.activityUntil = ''; await activityPage(); }
    else if (target.name === 'game') await gamePage(target.id);
    else gamesPage();
    if (version !== pageVersion) return;
    const focusTarget = target.focusR2 ? document.querySelector('#r2-form') : root;
    focusTarget?.focus({ preventScroll: true });
    if (target.focusR2) focusTarget?.scrollIntoView({ block: 'center' });
  } catch (error) { if (version === pageVersion) showError(error); }
}

// One live connection. History lives on the server and is read one page at a time.
let stream;
let hasConnected = false;
let refreshTimer;
let activityRequest = 0;
let activityDirty = false;
function acknowledgeUpdates() {
  clearTimeout(refreshTimer);
  refreshTimer = null;
  document.querySelector('#updates').hidden = true;
}
function scheduleRefresh() {
  if (document.hidden || refreshTimer) return;
  refreshTimer = setTimeout(async () => {
    refreshTimer = null;
    if (document.hidden) return;
    const version = pageVersion;
    try {
      await loadShared();
      if (version !== pageVersion) return;
      updateSyncControl();
      // Never replace a user's open form, selection, or dialog with a live redraw.
      const banner = document.querySelector('#updates');
      banner.hidden = false;
      banner.querySelector('span').textContent = 'New backup or library activity is available.';
    } catch { document.querySelector('#live-status').textContent = 'Could not refresh — reconnecting…'; }
  }, 750);
}
function connectEvents() {
  if (stream || document.hidden) return;
  stream = new EventSource('/api/events?live=true');
  stream.onopen = () => {
    document.querySelector('#live-status').textContent = 'Live updates connected';
    // Reconcile missed events after reconnect without replaying historical progress.
    state.syncProgress = { active: false, phase: 'starting', completed: 0, total: 0 };
    if (hasConnected) scheduleRefresh();
    hasConnected = true;
  };
  stream.onerror = () => { document.querySelector('#live-status').textContent = 'Reconnecting to SaveKnot…'; };
  stream.onmessage = message => {
    let event;
    try { event = JSON.parse(message.data); } catch { return; }
    if (!event || typeof event.type !== 'string') return;
    activityDirty = true;
    if (currentRoute().name === 'activity') {
      const button = document.querySelector('#refresh-activity');
      if (button) button.textContent = 'Show new activity';
    }
    if (event.type === 'sync.started') state.syncProgress = { active: true, phase: 'starting', completed: 0, total: 0 };
    if (event.type === 'sync.progress') state.syncProgress = { active: true, phase: event.data?.phase || 'starting', completed: Number(event.data?.completed || 0), total: Number(event.data?.total || 0) };
    if (event.type === 'sync.completed') {
      state.syncProgress = { active: false, phase: 'starting', completed: 0, total: 0 };
      if (state.status) state.status.syncInProgress = false;
    }
    updateSyncControl();
    if (['sync.completed', 'snapshot.completed', 'restore.completed', 'game.discovered'].includes(event.type)) scheduleRefresh();
  };
}
function disconnectEvents() { stream?.close(); stream = null; clearTimeout(refreshTimer); refreshTimer = null; }
window.addEventListener('pagehide', disconnectEvents);
window.addEventListener('pageshow', connectEvents);
document.addEventListener('visibilitychange', () => {
  if (document.hidden) disconnectEvents();
  else { state.syncProgress = { active: false, phase: 'starting', completed: 0, total: 0 }; connectEvents(); scheduleRefresh(); }
});
document.querySelector('#refresh-page').addEventListener('click', () => { document.querySelector('#updates').hidden = true; route(); });
document.querySelector('.skip-link').addEventListener('click', event => { event.preventDefault(); root.focus(); });
window.addEventListener('hashchange', () => { document.querySelector('#updates').hidden = true; route(); });
connectEvents();
route();
