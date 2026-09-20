export const escapeHTML = value => String(value ?? '').replace(/[&<>'"]/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', "'": '&#039;', '"': '&quot;' })[character]);
export const formatBytes = bytes => {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  const position = Math.max(0, Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1));
  return `${(bytes / 1024 ** position).toFixed(position ? 1 : 0)} ${units[position]}`;
};
const dateFormatter = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' });
export const formatTime = value => value && Number.isFinite(Date.parse(value)) ? dateFormatter.format(new Date(value)) : 'Never';
export function snapshotChanges(snapshot, previous) {
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

export function parseRoute(hash) {
  const parts = (hash || '#/games').replace(/^#\/?/, '').split('/');
  if (parts[0] === 'settings') return { name: 'settings', focusR2: parts[1] === 'r2' };
  if (parts[0] === 'activity') return { name: 'activity' };
  if (parts[0] === 'games' && parts[1]) {
    try { return { name: 'game', id: decodeURIComponent(parts[1]) }; } catch { /* Fall back to library. */ }
  }
  return { name: 'games' };
}

export function syncProgressValues(progress) {
  if (!Number.isFinite(progress.total) || progress.total <= 0 || !Number.isFinite(progress.completed)) return null;
  return { max: progress.total, value: Math.min(Math.max(progress.completed, 0), progress.total) };
}

export function pageItems(items, page, size) {
  const pages = Math.max(1, Math.ceil(items.length / size));
  const current = Math.min(Math.max(page, 0), pages - 1);
  return { items: items.slice(current * size, (current + 1) * size), page: current, pages, total: items.length };
}

export async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    cache: 'no-store',
    headers: options.body instanceof FormData ? options.headers : { 'Content-Type': 'application/json', ...(options.headers || {}) },
  });
  if (response.status === 204) return null;
  let data;
  try { data = await response.json(); } catch { throw new Error(`Invalid server response (${response.status}). Try again.`); }
  if (!response.ok) {
    const error = new Error(data.error || `Request failed (${response.status})`);
    error.status = response.status;
    throw error;
  }
  return data;
}

export function filterGames(games, filters) {
  const query = filters.query.trim().toLocaleLowerCase();
  return games.filter(game => {
    const matchesQuery = !query || [game.displayName, game.catalogName].some(value => String(value || '').toLocaleLowerCase().includes(query));
    const matchesWatcher = filters.watcher === 'all' || filters.watcher === 'watched' && game.enabled || filters.watcher === 'manual' && !game.enabled;
    const matchesSync = filters.sync === 'all' || filters.sync === 'pending' && game.pendingSnapshotCount > 0 || filters.sync === 'current' && game.pendingSnapshotCount === 0;
    const matchesSnapshots = filters.snapshots === 'all' || filters.snapshots === 'yes' && game.snapshotCount > 0 || filters.snapshots === 'no' && game.snapshotCount === 0;
    return matchesQuery && matchesWatcher && matchesSync && matchesSnapshots;
  });
}

export function activityText(event, gameName = '') {
  const subject = gameName ? ` for ${gameName}` : '';
  const result = event?.data?.result || {};
  const labels = {
    'catalog.failed': ['Game catalog update failed', event.message],
    'discovery.failed': ['Game scan failed', event.message],
    'game.discovered': [`Added ${gameName || 'a game'}`, 'Found during a game scan'],
    'restore.completed': [`Restore completed${subject}`, 'The previous files were backed up first'],
    'snapshot.completed': [`Backup created${subject}`, 'Saved locally'],
    'snapshot.deleted': [`Backup deleted${subject}`, 'Removed from the selected locations'],
    'snapshot.failed': [`Backup failed${subject}`, event.message],
    'snapshot.retained': [`Old backup removed${subject}`, 'Removed by the retention limit'],
    'storage.error': ['Local storage error', event.message],
    'storage.reconcile.completed': ['R2 history refreshed', 'Remote backups are up to date on this device'],
    'sync.completed': ['Sync finished', `${Number(result.createdSnapshots || 0)} new backups · ${Number(result.synced || 0)} uploaded${event.message ? ' · Some items need attention' : ''}`],
    'upload.completed': [`Uploaded backup${subject}`, 'Stored in R2'],
    'upload.failed': [`Upload failed${subject}`, event.message],
  };
  return labels[event?.type] || ['SaveKnot activity', event?.message || 'Completed'];
}
