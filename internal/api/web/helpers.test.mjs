import { test } from 'node:test';
import assert from 'node:assert/strict';
import { api, escapeHTML, formatBytes, formatTime, snapshotChanges, parseRoute, syncProgressValues, pageItems, filterGames, activityText } from './helpers.mjs';

test('snapshot comparison distinguishes sources, changes, and removals', () => {
  const previous = { files: [{ sourceKey: 'a', path: 'same', hash: '1' }, { path: 'changed', hash: '1' }, { path: 'removed', hash: '1' }] };
  const current = { files: [{ sourceKey: 'a', path: 'same', hash: '1' }, { sourceKey: 'b', path: 'same', hash: '1' }, { path: 'changed', hash: '2' }] };
  assert.deepEqual(snapshotChanges(current, previous).map(file => file.state), ['unchanged', 'added', 'changed', 'removed']);
});
test('formats missing and invalid data safely', () => {
  assert.equal(formatBytes(1536), '1.5 KB');
  for (const value of [0, -1, NaN, Infinity]) assert.equal(formatBytes(value), '0 B');
  assert.equal(formatTime('invalid'), 'Never');
  assert.equal(escapeHTML('<img src="x" onerror=\'bad\'> &'), '&lt;img src=&quot;x&quot; onerror=&#039;bad&#039;&gt; &amp;');
});
test('routing handles encoded IDs, malformed routes and R2 focus', () => {
  assert.deepEqual(parseRoute('#/games/game%2Fid'), { name: 'game', id: 'game/id' });
  assert.deepEqual(parseRoute('#/settings/r2'), { name: 'settings', focusR2: true });
  for (const value of ['', '#/unknown', '#/games/%ZZ']) assert.deepEqual(parseRoute(value), { name: 'games' });
});
test('progress is always finite and clamped', () => {
  for (const progress of [{ total: 0, completed: 0 }, { total: 3, completed: NaN }, { total: Infinity, completed: 4 }]) assert.equal(syncProgressValues(progress), null);
  assert.deepEqual(syncProgressValues({ total: 3, completed: 4 }), { max: 3, value: 3 });
  assert.deepEqual(syncProgressValues({ total: 3, completed: -4 }), { max: 3, value: 0 });
});
test('large lists stay bounded and the final page remains reachable', () => {
  const items = Array.from({ length: 10001 }, (_, i) => i);
  assert.equal(pageItems(items, 0, 36).items.length, 36);
  const last = pageItems(items, 99999, 36);
  assert.equal(last.items.at(-1), 10000);
  assert.ok(last.items.length <= 36);
  assert.deepEqual(pageItems([], 8, 36), { items: [], page: 0, pages: 1, total: 0 });
});
test('library filters combine catalog search, backup mode, pending uploads and backups', () => {
  const games = [{ id: 'a', displayName: 'First', catalogName: 'Catalog', enabled: true, pendingSnapshotCount: 0, snapshotCount: 2 }, { id: 'b', displayName: 'Second', catalogName: 'Other', enabled: false, pendingSnapshotCount: 1, snapshotCount: 0 }];
  const filters = { query: ' CATALOG ', watcher: 'all', sync: 'all', snapshots: 'all' };
  assert.deepEqual(filterGames(games, filters).map(game => game.id), ['a']);
  assert.deepEqual(filterGames(games, { query: '', watcher: 'manual', sync: 'pending', snapshots: 'no' }).map(game => game.id), ['b']);
});
test('activity labels turn internal event names into useful user-facing text', () => {
  assert.deepEqual(activityText({ type: 'snapshot.completed' }, 'Abyssus'), ['Backup created for Abyssus', 'Saved locally']);
  assert.deepEqual(activityText({ type: 'upload.failed', message: 'network unavailable' }, 'Abyssus'), ['Upload failed for Abyssus', 'network unavailable']);
  assert.deepEqual(activityText({ type: 'sync.completed', data: { result: { createdSnapshots: 2, synced: 3 } } }), ['Sync finished', '2 new backups · 3 uploaded']);
});
test('transport preserves no-content, multipart headers, JSON errors and cache policy', async (t) => {
  const calls = [];
  t.mock.method(globalThis, 'fetch', async (...args) => { calls.push(args); return new Response(null, { status: 204 }); });
  assert.equal(await api('/api/folder', { method: 'POST', body: '{}' }), null);
  assert.equal(calls[0][1].cache, 'no-store');
  assert.equal(calls[0][1].headers['Content-Type'], 'application/json');
  await api('/api/image', { method: 'POST', body: new FormData() });
  assert.equal(calls[1][1].headers, undefined);
  globalThis.fetch = async () => new Response(JSON.stringify({ error: 'busy' }), { status: 409 });
  await assert.rejects(api('/api/sync'), { message: 'busy', status: 409 });
  globalThis.fetch = async () => new Response('<html>broken</html>');
  await assert.rejects(api('/api/status'), /Invalid server response/);
});
