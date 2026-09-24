import { test } from 'node:test';
import assert from 'node:assert/strict';
import { activityEvents, createAccentPreference, navigationTransition, runNavigationUpdate } from './ui-behavior.mjs';

const accents = ['lime', 'violet', 'coral'];

function classList() {
  const names = new Set();
  return {
    add: name => names.add(name),
    remove: name => names.delete(name),
    contains: name => names.has(name),
  };
}

function storage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: key => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, value),
    value: key => values.get(key),
  };
}

test('accent preference restores a saved selection and applies a new one immediately', () => {
  const saved = storage({ 'saveknot.accent': 'violet' });
  const root = { dataset: {} };
  const preference = createAccentPreference({ root, storage: saved, accents, fallback: 'lime' });

  assert.equal(preference.current, 'violet');
  assert.equal(root.dataset.accent, 'violet');

  preference.select('coral');
  assert.equal(preference.current, 'coral');
  assert.equal(root.dataset.accent, 'coral');
  assert.equal(saved.value('saveknot.accent'), 'coral');
});

test('invalid stored or selected accents cannot set an unsupported theme', () => {
  const saved = storage({ 'saveknot.accent': 'obsolete' });
  const root = { dataset: {} };
  const preference = createAccentPreference({ root, storage: saved, accents, fallback: 'lime' });

  assert.equal(preference.current, 'lime');
  assert.equal(root.dataset.accent, 'lime');
  preference.select('not-a-theme');
  assert.equal(preference.current, 'lime');
  assert.equal(root.dataset.accent, 'lime');
  assert.notEqual(saved.value('saveknot.accent'), 'not-a-theme');
});

test('accent selection remains usable if browser storage is unavailable', () => {
  const unavailable = {
    getItem() { throw new Error('storage disabled'); },
    setItem() { throw new Error('storage disabled'); },
  };
  const root = { dataset: {} };
  const preference = createAccentPreference({ root, storage: unavailable, accents, fallback: 'lime' });

  preference.select('violet');
  assert.equal(preference.current, 'violet');
  assert.equal(root.dataset.accent, 'violet');
});

test('a clicked game card uses the card-to-detail transition only for its destination', () => {
  const from = { name: 'games' };
  const to = { name: 'game', id: 'game/id' };
  assert.equal(navigationTransition({ from, to, cardId: 'game/id' }), 'card');
  assert.equal(navigationTransition({ from, to, cardId: 'another-game' }), 'route');
  assert.equal(navigationTransition({ from, to }), 'route');
});

test('other route changes animate, while same-route renders and reduced motion do not', () => {
  assert.equal(navigationTransition({ from: { name: 'games' }, to: { name: 'activity' } }), 'route');
  assert.equal(navigationTransition({ from: { name: 'settings' }, to: { name: 'settings' } }), 'none');
  assert.equal(navigationTransition({ from: { name: 'games' }, to: { name: 'game', id: 'a' }, cardId: 'a', reducedMotion: true }), 'none');
  assert.equal(navigationTransition({ from: { name: 'activity' }, to: { name: 'games' }, reducedMotion: true }), 'none');
});

test('failed async route updates propagate after the view transition settles', async () => {
  const failure = new Error('shared API unavailable');
  let callback;
  const document = {
    documentElement: { dataset: {} },
    startViewTransition(update) {
      callback = update;
      return { finished: Promise.resolve().then(() => callback()) };
    },
  };

  await assert.rejects(runNavigationUpdate({
    document,
    root: { querySelectorAll: () => [] },
    from: { name: 'settings' },
    to: { name: 'activity' },
    update: async () => { throw failure; },
  }), error => error === failure);

  assert.equal(typeof callback, 'function');
  assert.equal(document.documentElement.dataset.navigationTransition, undefined);
});

test('failed updates still propagate without View Transitions support', async () => {
  const failure = new Error('shared API unavailable');
  await assert.rejects(runNavigationUpdate({
    document: { documentElement: { dataset: {} } },
    root: {},
    from: { name: 'settings' },
    to: { name: 'activity' },
    update: async () => { throw failure; },
  }), error => error === failure);
});

test('card transition pairs the clicked cover with the rendered detail cover', async () => {
  const source = { classList: classList() };
  const destination = { classList: classList() };
  let rendered = false;
  let oldName;
  let newName;
  const root = {
    querySelectorAll: () => rendered ? [] : [{ dataset: { gameId: 'a' }, querySelector: () => source }],
    querySelector: () => rendered ? destination : null,
  };
  const document = {
    documentElement: { dataset: {} },
    startViewTransition(update) {
      oldName = source.classList.contains('shared-game-art');
      return { finished: Promise.resolve().then(update).then(() => { newName = destination.classList.contains('shared-game-art'); }) };
    },
  };
  await runNavigationUpdate({
    document, root, from: { name: 'games' }, to: { name: 'game', id: 'a' }, cardId: 'a',
    update: async () => { await Promise.resolve(); rendered = true; },
  });
  assert.equal(oldName, true);
  assert.equal(newName, true);
  assert.equal(source.classList.contains('shared-game-art'), false);
  assert.equal(destination.classList.contains('shared-game-art'), false);
  assert.equal(document.documentElement.dataset.navigationTransition, undefined);
});

test('a missing clicked card falls back to ordinary route motion', async () => {
  let kind;
  const document = {
    documentElement: { dataset: {} },
    startViewTransition(update) {
      kind = this.documentElement.dataset.navigationTransition;
      return { finished: Promise.resolve().then(update) };
    },
  };
  await runNavigationUpdate({
    document, root: { querySelectorAll: () => [], querySelector: () => null },
    from: { name: 'games' }, to: { name: 'game', id: 'missing' }, cardId: 'missing',
    update: async () => {},
  });
  assert.equal(kind, 'route');
});

test('reduced-motion card navigation updates without starting a view transition', async () => {
  const source = { classList: classList() };
  let started = false;
  let updated = false;
  await runNavigationUpdate({
    document: {
      documentElement: { dataset: {} },
      startViewTransition() { started = true; throw new Error('unexpected transition'); },
    },
    root: { querySelectorAll: () => [{ dataset: { gameId: 'a' }, querySelector: () => source }] },
    from: { name: 'games' }, to: { name: 'game', id: 'a' }, cardId: 'a',
    reducedMotion: true,
    update: async () => { updated = true; },
  });
  assert.equal(updated, true);
  assert.equal(started, false);
  assert.equal(source.classList.contains('shared-game-art'), false);
});

test('returning from a game detail to the library selects reverse card motion', () => {
  assert.equal(navigationTransition({ from: { name: 'game', id: 'a' }, to: { name: 'games' }, cardId: 'a' }), 'card-return');
  assert.equal(navigationTransition({ from: { name: 'game', id: 'a' }, to: { name: 'games' } }), 'route');
  assert.equal(navigationTransition({ from: { name: 'game', id: 'a' }, to: { name: 'games' }, cardId: 'other' }), 'route');
  assert.equal(navigationTransition({ from: { name: 'game', id: 'a' }, to: { name: 'activity' } }), 'route');
  assert.equal(navigationTransition({ from: { name: 'game', id: 'a' }, to: { name: 'games' }, cardId: 'a', reducedMotion: true }), 'none');
});

test('reverse transition pairs the detail hero with only its original library card', async () => {
  const hero = { classList: classList() };
  const target = { classList: classList() };
  const other = { classList: classList() };
  let rendered = false;
  let restored = false;
  let oldNamed;
  let targetNamed;
  let otherNamed;
  const root = {
    querySelector: selector => selector === '.detail-cover' && !rendered ? hero : null,
    querySelectorAll: () => rendered ? [
      { dataset: { gameId: 'other' }, querySelector: () => other },
      { dataset: { gameId: 'a' }, querySelector: () => target },
    ] : [],
  };
  const document = {
    documentElement: { dataset: {} },
    startViewTransition(update) {
      oldNamed = hero.classList.contains('shared-game-art');
      return { finished: Promise.resolve().then(update).then(() => {
        assert.equal(restored, true);
        targetNamed = target.classList.contains('shared-game-art');
        otherNamed = other.classList.contains('shared-game-art');
      }) };
    },
  };
  await runNavigationUpdate({
    document, root, from: { name: 'game', id: 'a' }, to: { name: 'games' }, cardId: 'a',
    update: async () => { await Promise.resolve(); rendered = true; },
    restorePosition: () => { assert.equal(rendered, true); restored = true; },
  });
  assert.equal(oldNamed, true);
  assert.equal(targetNamed, true);
  assert.equal(otherNamed, false);
  assert.equal(hero.classList.contains('shared-game-art'), false);
  assert.equal(target.classList.contains('shared-game-art'), false);
  assert.equal(document.documentElement.dataset.navigationTransition, undefined);
});

test('reverse motion falls back to a route transition when the detail hero is absent', async () => {
  let kind;
  let rendered = false;
  let restored = false;
  const document = {
    documentElement: { dataset: {} },
    startViewTransition(update) {
      kind = this.documentElement.dataset.navigationTransition;
      return { finished: Promise.resolve().then(update).then(() => { assert.equal(restored, true); }) };
    },
  };
  await runNavigationUpdate({
    document, root: { querySelector: () => null, querySelectorAll: () => [] },
    from: { name: 'game', id: 'a' }, to: { name: 'games' }, cardId: 'a',
    update: async () => { rendered = true; },
    restorePosition: () => { assert.equal(rendered, true); restored = true; },
  });
  assert.equal(kind, 'route');
  assert.equal(restored, true);
});

test('reverse motion does not claim a shared transition if its card is absent after rendering', async () => {
  const hero = { classList: classList() };
  let rendered = false;
  let settledKind;
  const document = {
    documentElement: { dataset: {} },
    startViewTransition(update) {
      return { finished: Promise.resolve().then(update).then(() => {
        settledKind = this.documentElement.dataset.navigationTransition;
      }) };
    },
  };
  await runNavigationUpdate({
    document,
    root: {
      querySelector: () => rendered ? null : hero,
      querySelectorAll: () => [],
    },
    from: { name: 'game', id: 'a' }, to: { name: 'games' },
    cardId: 'a',
    update: async () => { rendered = true; },
  });
  assert.equal(settledKind, 'route');
  assert.equal(hero.classList.contains('shared-game-art'), false);
});

test('reduced motion returns to the library without starting reverse card animation', async () => {
  let updated = false;
  let restored = false;
  await runNavigationUpdate({
    document: { documentElement: { dataset: {} }, startViewTransition() { throw new Error('unexpected transition'); } },
    root: {}, from: { name: 'game', id: 'a' }, to: { name: 'games' }, cardId: 'a', reducedMotion: true,
    update: async () => { updated = true; },
    restorePosition: () => { assert.equal(updated, true); restored = true; },
  });
  assert.equal(updated, true);
  assert.equal(restored, true);
});

test('activity payloads with null or invalid events render as an empty list', () => {
  assert.deepEqual(activityEvents(null), []);
  assert.deepEqual(activityEvents(undefined), []);
  const events = [{ id: 'evt-1' }];
  assert.equal(activityEvents(events), events);
});

