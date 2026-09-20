# Embedded UI development

The UI source is `internal/api/web/`: `index.html`, `styles.css`, `app.js`, and `helpers.mjs`. Go embeds these files directly; no framework, npm installation, bundler, generated assets, or separate dev server is needed.

- `go run ./cmd/saveknot`: run the app (restart after editing embedded assets).
- `make build`: compile the single executable using Go only.
- `make ui-check`: syntax and regression checks, using Node.js 22+ and its built-in test runner.
- `go test ./...`: backend and embedded asset checks.

`app.js` owns explicit DOM updates, route guards, native dialogs, and one live event connection. `helpers.mjs` contains pure presentation helpers. Escape all API values inserted into markup. Keep async rendering guarded against navigation; refresh controls must preserve form drafts. The live event stream does not retain client history: `/api/activity` serves 50 entries at a time, anchored to a timestamp so new events do not shift older pages. Hidden tabs close their event connection and refresh shared status when visible again.

Games render 36 per page, snapshots 25 per page, and expanded file lists 100 per page. Artwork is lazy loaded. Snapshot manifests are still fetched per game; the DOM is bounded, while that API payload grows with snapshot history. The server retains its existing 30-day activity history and persistence behavior.
