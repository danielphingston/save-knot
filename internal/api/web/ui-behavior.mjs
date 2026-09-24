export function createAccentPreference({ root, storage, accents, fallback, key = 'saveknot.accent' }) {
  const allowed = new Set(accents);
  let current = fallback;

  try {
    const saved = storage?.getItem(key);
    if (allowed.has(saved)) current = saved;
  } catch {
    // Theme selection remains available when browser storage is blocked.
  }

  const apply = accent => {
    if (!allowed.has(accent)) return current;
    current = accent;
    root.dataset.accent = accent;
    try { storage?.setItem(key, accent); } catch { /* Keep the selection in memory. */ }
    return current;
  };

  root.dataset.accent = current;
  return {
    get current() { return current; },
    select: apply,
  };
}

export function navigationTransition({ from, to, cardId, reducedMotion = false }) {
  if (reducedMotion) return 'none';
  if (!to || from?.name === to.name && (to.name !== 'game' || from?.id === to.id)) return 'none';
  if (from?.name === 'game' && to.name === 'games' && cardId && cardId === from.id) return 'card-return';
  if (from?.name === 'games' && to.name === 'game' && cardId && cardId === to.id) return 'card';
  return 'route';
}

export async function runNavigationUpdate({ document, root, from, to, cardId, reducedMotion, update, restorePosition }) {
  const reverseCandidate = from?.name === 'game' && to?.name === 'games' && Boolean(cardId) && cardId === from.id;
  let kind = navigationTransition({ from, to, cardId, reducedMotion });
  if (kind === 'none' || typeof document.startViewTransition !== 'function') {
    await update();
    if (reverseCandidate) await restorePosition?.();
    return;
  }
  let sourceArt;
  let destinationArt;
  if (kind === 'card') {
    const card = [...root.querySelectorAll('.game-card[data-game-id]')].find(item => item.dataset.gameId === cardId);
    sourceArt = card?.querySelector('.cover');
    if (!sourceArt) kind = 'route';
  } else if (kind === 'card-return') {
    sourceArt = root.querySelector('.detail-cover');
    if (!sourceArt) kind = 'route';
  }
  document.documentElement.dataset.navigationTransition = kind;
  if (kind === 'card' || kind === 'card-return') sourceArt.classList.add('shared-game-art');
  let updateError;
  let transition;
  const transitionKind = kind;
  try {
    transition = document.startViewTransition(async () => {
      try {
        await update();
        if (transitionKind === 'card') {
          destinationArt = root.querySelector('.detail-cover');
          destinationArt?.classList.add('shared-game-art');
        } else if (transitionKind === 'card-return') {
          await restorePosition?.();
          const card = [...root.querySelectorAll('.game-card[data-game-id]')].find(item => item.dataset.gameId === cardId);
          destinationArt = card?.querySelector('.cover');
          if (destinationArt) destinationArt.classList.add('shared-game-art');
          else {
            kind = 'route';
            document.documentElement.dataset.navigationTransition = kind;
            sourceArt.classList.remove('shared-game-art');
          }
        }
        if (reverseCandidate && transitionKind !== 'card-return') await restorePosition?.();
      } catch (error) { updateError = error; }
    });
  } catch {
    delete document.documentElement.dataset.navigationTransition;
    sourceArt?.classList.remove('shared-game-art');
    await update();
    if (reverseCandidate) await restorePosition?.();
    return;
  }
  try { await transition.finished.catch(() => {}); }
  finally {
    delete document.documentElement.dataset.navigationTransition;
    sourceArt?.classList.remove('shared-game-art');
    destinationArt?.classList.remove('shared-game-art');
  }
  if (updateError) throw updateError;
}

export function activityEvents(events) {
  return Array.isArray(events) ? events : [];
}
