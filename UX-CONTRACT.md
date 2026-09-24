# SaveKnot UX Contract

## Product context

- **Audience:** Desktop players managing local game-save backups and optional R2 sync.
- **Primary jobs:** Find a game, inspect saved versions, restore a version, and configure local/cloud backup behavior.
- **Locale:** English interface; dates use the browser locale.
- **Accessibility target:** Semantic HTML, keyboard navigation, visible focus, and reduced-motion support.
- **Business context:** `README.md` describes the local-first product and `internal/api/server.go` defines the loopback UI/API boundary and lifecycle endpoints.

## Canonical UI map

| Capability | Canonical owner | Source of truth | Verification |
|---|---|---|---|
| Route/navigation | Hash routes in `helpers.mjs`, dispatch in `app.js` | `parseRoute` and `route` | `make ui-check` |
| Game list/details | `gamesPage` and `gamePage` | Server API and paginated responses | UI + server tests |
| Forms | Native form controls and route handlers | API validation and existing form submissions | UI + server tests |
| Confirmation | App dialog / guarded action handler | Action consequence text and endpoint | UI review |
| Toast | `#toast-region` and `toast()` | Shared live region | UI review |
| Accent preference | `ui-behavior.mjs` | Validated local preference, lime fallback | UI behavior tests |
| Route transition | `ui-behavior.mjs` + `document.startViewTransition` | Route identity and reduced-motion setting | UI behavior tests |

## Behavior invariants

- Games, Activity, and Settings remain in the shared left navigation; game detail stays under Games.
- The Games library and Activity log retain bounded server/UI pagination and existing filters.
- Game cards navigate to their matching detail route. The clicked card may use a named cover transition; other route changes use the route transition. Unsupported browsers and reduced-motion users get immediate navigation.
- Navigation preserves the URL hash, closes open dialogs, and focuses the destination content. A failed detail request keeps an actionable error page.
- Game backup, restore, removal, snapshot deletion, path changes, and R2 disconnect keep their existing explicit confirmation or validation guards.
- Accent selection updates immediately and stores only one validated visual preference. Storage failure leaves the in-memory selection usable. Lime is the fallback.
- Forms retain their current endpoints, field names, async error feedback, and successful destinations.

