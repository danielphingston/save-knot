# SaveKnot

SaveKnot is a tiny, local-first game-save daemon. It uses the [Ludusavi manifest](https://github.com/mtkennerly/ludusavi-manifest) to discover game saves, watches their locations, creates immutable content-addressed snapshots, and sends them directly to your own Cloudflare R2 bucket.

There is no SaveKnot account, hosted backend, central database, analytics service, or remote control plane.

## What works

- Ludusavi manifest download with ETag caching and validation before activation
- Streaming compilation into a compact SQLite catalog index; the full YAML object graph is never retained
- Steam discovery from Windows registry entries, standard roots, and `libraryfolders.vdf`
- Epic launcher manifest discovery, GOG root discovery, and a bounded deep scan for existing local saves
- Ludusavi aliases, extra store IDs, secondary `.ludusavi.yaml` manifests, registry rules, placeholders, constraints, and globs
- Manual games and arbitrary absolute save paths
- Filesystem notifications with configurable quiet debounce, minimum snapshot gap, and maximum dirty duration
- Stable-read checks to avoid half-written game saves
- SHA-256 content addressing and zstd compression
- Immutable local snapshot history in SQLite
- Direct, bucket-scoped R2 uploads; blobs publish before snapshot manifests
- Startup/manual R2 history reconciliation with lazy, integrity-checked blob downloads
- Separate local backup, remote sync, restore, and local/remote delete actions
- Conservative restores with a pre-restore snapshot and SHA-256 verification
- Recursive Windows registry backup and restore
- Custom titles, notes, catalog remapping, pictures, save locations, file exclusions, and per-game backup policy
- Configurable local backup location, per-game retention, periodic watched-game sync, optional game discovery, and per-user launch at login
- On-demand sync that checkpoints changed watched games before uploading their pending snapshots
- Embedded localhost UI, Windows tray controls, native folder picker, SQLite-cached discovery diagnostics, and a local activity log retained for 30 days across restarts
- Persistent rotating logs at `<data directory>/logs/saveknot.log`
- OS credential-vault storage for the R2 secret access key

## Run it

Requirements: Go 1.26 or newer. Node.js 22 or newer is only needed to run the optional UI regression tests; building the application needs no Node.js or npm.

```sh
go run ./cmd/saveknot
```

Open <http://127.0.0.1:32147>. SaveKnot stores its state beneath the operating system's user configuration directory. For an isolated development instance:

```sh
go run ./cmd/saveknot -data-dir ./.cache/dev -listen 127.0.0.1:32147
```

To cross-compile a Windows AMD64 executable from Linux or macOS:

```sh
make build-windows
```

The executable is written to `dist/saveknot-windows-amd64.exe`.

The plain HTML, CSS, and JavaScript UI lives in `internal/api/web/` and is embedded directly by Go. Run `make build` after editing it, or restart `go run ./cmd/saveknot`. There is no frontend bundler, framework, package installation, or separate development server. `make ui-check` runs dependency-free JavaScript checks and regression tests.

The UI renders games, snapshots, file lists, and activity in bounded pages. Artwork loads lazily; file details exist only while expanded. Live updates use one event connection, pause while the tab is hidden, and preserve open forms. The complete 30-day activity log remains available through paginated history.

The Windows release is linked as a GUI/background executable, so it does not leave a console window open. While it is running, use the SaveKnot notification-area icon to open the local webpage or exit cleanly. For troubleshooting, inspect `%APPDATA%\SaveKnot\logs\saveknot.log`; use `-log-level debug` from PowerShell for more detail.

SaveKnot deliberately rejects non-loopback listen addresses.

## Publish a release

Push a semantic-version tag to build and publish a Windows AMD64 package through GitHub Actions:

```sh
git tag -a v0.1.0 -m "SaveKnot v0.1.0"
git push origin v0.1.0
```

The workflow runs the tests, creates `saveknot_0.1.0_windows_amd64.zip` and `SHA256SUMS.txt`, and attaches them to a GitHub Release with generated release notes. A tag with a suffix such as `v0.2.0-rc.1` creates a prerelease. The workflow uses the repository's built-in `GITHUB_TOKEN`; no publishing secret is required.

## If your game list is empty

Open **Settings → Discovery diagnostics**, then select **Scan now**. The panel separates the stages so an empty list is actionable:

- **Catalog** confirms that the Ludusavi manifest downloaded and shows the parsed game count.
- **Steam roots** shows every detected/configured Steam installation.
- **Installed** counts launcher manifests found for Steam, Epic, and GOG.
- **Matched in Ludusavi** shows how many launcher entries mapped to catalog definitions.
- **Found from local save data** reports the bounded deep scan for saves from games not found through a launcher.
- **Unmatched** names and persistent logs explain remaining misses; add a non-standard launcher root or create a manual game when necessary.

Ludusavi is the save-definition catalog, not a rich store-metadata API. SaveKnot consumes its canonical title, aliases, store IDs, installation aliases, file/registry rules, constraints, and path placeholders. Steam cover art is derived separately from a matched Steam app ID; custom pictures always override it. Ludusavi does not supply descriptions or cover images.

Game discovery is manual by default. SaveKnot refreshes its compact catalog in the background, and searches launchers and local save locations when you select **Scan for games** or **Scan now**. You can independently enable a periodic search in **Settings → Automatic sync & discovery**. Between scans, it watches only enabled save locations already registered in the local database.

The Games tab highlights titles with no readable save files at their configured locations. Open a game to inspect each location or add one manually. Use the **Game view** selector to switch between the library and ignored games; restoring an ignored game keeps its snapshots and customizations.

## Connect R2

1. Create an R2 bucket in Cloudflare.
2. Create an S3 API token with **Object Read & Write** access scoped to that bucket.
3. Open **Settings** in SaveKnot.
4. Enter the account ID, bucket, access key ID, secret access key, and optional object prefix.
5. Select **Test & connect**. Settings are only committed after SaveKnot verifies bucket access plus list, put, get, and delete using a temporary capability object.

The secret is stored with Windows Credential Manager, macOS Keychain, or the Linux Secret Service through the OS keyring. Non-secret connection metadata lives in `config.json` with user-only permissions.

R2 objects use this layout:

```text
<prefix>/v1/
├── blobs/sha256/<first-two-hash-characters>/<sha256>.zst
└── games/<game-id>/
    ├── snapshots/<snapshot-id>.json
    └── active-selections/<event-id>.json
```

Blobs are immutable by their SHA-256 name. A snapshot becomes visible remotely only after all of its blobs have uploaded successfully.

`ListObjectsV2` is not used by the watcher or save tracking. The connection test makes one `MaxKeys=1` capability-list request under its temporary probe prefix. SaveKnot lists snapshot manifests only at startup when R2 is configured or when you explicitly select **Refresh from R2**. Already-known immutable snapshot IDs are not downloaded again. Blob uploads use the local SQLite hash index during normal operation.

## Architecture

SaveKnot borrows Cordis's strongest architectural idea: runtime capabilities have explicit owners and lifetimes. It does not reproduce Cordis's dynamic context, proxy, or service-location machinery.

```text
Application lifecycle
├── catalog + store/local-save discovery coordinator
├── filesystem watcher
├── snapshot and restore service
├── R2 sync service
├── typed event bus
├── SQLite state store
└── local HTTP API + embedded UI
```

Interfaces exist only at real I/O boundaries: repository consumption, object storage, and secret storage. Concrete types are used everywhere else. The initial modules are compiled into one binary; a future external plugin boundary should use a versioned process or WASM protocol rather than Go's platform-limited `plugin` ABI.

The database is local operational state. The immutable objects in R2 are the durable backup format.

### R2 and multiple computers

Each installation keeps its own database, device ID, save locations, and local
blobs. Connecting the same R2 bucket and object prefix imports remote snapshot
history and active-selection events at startup; **Refresh from R2** imports them
on demand. Remote history is refreshed at those boundaries, not on every
background sync. Snapshot blobs download only when you restore a snapshot or
export it as a ZIP. The archive includes the manifest and save files and can be
downloaded even when that game or its save paths are not installed on this
computer.

The snapshot history lets you compare versions by device, date, file changes,
and size. Choose **Set active master** on the version you want to prefer. This
writes an immutable selection event to R2 when connected; otherwise it stays
local and uploads on the next **Sync now**. Other computers learn about that
choice at startup or the next **Refresh from R2**. If devices select different
versions while offline, all events remain available and SaveKnot resolves the
effective choice deterministically by selection time, then device ID and event
ID. Choosing a master changes the preferred version; it does not merge or
rewrite save files. You can still compare, download, or restore the other
versions. Restore first captures the current files as a local pre-restore
snapshot.

The destination computer needs a matching save location configured to restore
a snapshot. Ludusavi paths use their logical template, so the resolved absolute
folder may differ by computer. Custom paths use their absolute path as the
source identity; restoring a custom-path snapshot to a different path requires
matching that original path. A custom game created separately on each computer
also gets a different game ID. Connect R2 and import the existing remote game
before adding its destination save location.

Snapshot retention removes only this device's excess unsynced snapshots. Synced snapshots, versions created on other devices, and snapshots referenced by selection events remain preserved until you explicitly delete them. The active master must be changed before its snapshot can be deleted. **Sync now** and configured periodic syncs
upload pending snapshots and selection events; watched games can also upload a
new snapshot after a save changes.

Run the two-computer R2 protocol test locally with Docker:

```sh
make test-r2-e2e
```

This starts a disposable MinIO S3 server and bucket, creates separate local
databases and save folders, tests backup/upload/import/download/restore in both
directions, verifies pre-restore recovery and repeated reconciliation, rejects
a damaged blob, then removes the container. Wrangler's local R2 binding does
not expose the S3 API used by SaveKnot. To run the same test against a real R2
bucket instead, set `SAVEKNOT_E2E_R2_ACCOUNT_ID`, `SAVEKNOT_E2E_R2_BUCKET`,
`SAVEKNOT_E2E_R2_ACCESS_KEY_ID`, and `SAVEKNOT_E2E_R2_SECRET_ACCESS_KEY`, then run
`go test ./internal/remote -run '^TestR2TwoDevicesEndToEnd$' -count=1 -v`.
The test uses a unique object prefix and removes its test objects afterward.

## Quality gate

Run the complete pinned quality gate (the first run downloads the linter into Go's module cache):

```sh
make check
```

### Codex cloud environment

The Codex universal image may offer an older Go preset than this repository's Go 1.26 requirement. In the Codex cloud environment settings, leave the preinstalled Go version at its default and use this for both the **Setup script** and **Maintenance script**:

```sh
bash scripts/setup-codex-cloud.sh
```

Paste only that one command, not the contents of the script. Codex checks out the repository before running environment setup, so the command executes the version tracked with the project.

The script resolves and installs the latest official stable Go toolchain with checksum verification, rejects versions older than `go.mod`, clears stale `GOROOT` state from the preinstalled runtime, verifies that the compiler matches the selected Go release, installs the system compiler needed by `go test -race`, prefetches module dependencies, and installs the pinned `golangci-lint` release while setup networking is available. It also installs the latest `agent-browser` CLI, a proxy-compatible system Chrome package, Linux browser dependencies, and the bundled core browser skill, then launches an isolated smoke-test session so cloud agents can perform UI verification. It is safe to run again when Codex resumes a cached environment. Set `SAVEKNOT_GO_VERSION` or `SAVEKNOT_AGENT_BROWSER_VERSION` in the environment settings only when you intentionally want to pin a specific release. If the script body must be pasted directly, it can locate the checkout from the current working directory or `SAVEKNOT_REPO_ROOT`.

The gate runs the curated correctness, error-handling, context, resource, complexity, hygiene, and security linters from [`GO_AI_CODE_QUALITY.md`](GO_AI_CODE_QUALITY.md), plus normal and race tests. CI also verifies formatting, coverage reporting, `go mod tidy`, and clean diffs.

Dependencies are intentionally narrow and each owns a boundary the standard library does not cover:

- AWS SDK for Go v2: supported R2/S3 protocol client
- modernc SQLite: embedded, CGO-free state database
- fsnotify: cross-platform filesystem notifications
- yaml.v3: Ludusavi manifest decoding
- doublestar: Ludusavi-compatible recursive glob expansion
- klauspost/compress: zstd blob encoding
- go-keyring: native operating-system credential storage
- x/sys: Windows registry discovery, backup/restore, and per-user startup integration

## Scope and tradeoffs

- The embedded UI uses plain HTML, CSS, and JavaScript with native dialogs and no runtime dependencies. DOM updates are explicit; regression tests cover helpers and backend contracts. Large lists are paginated to limit browser memory.
- Discovery checks store manifests first. The deeper catalog scan is bounded and only evaluates user-anchored rules whose literal parent directory exists; install-root rules are not expanded blindly across the full catalog.
- Snapshot manifests reference logical source keys, not absolute catalog paths. A restore requires the corresponding save location to be configured on that device.
- R2 reconciliation lists snapshot manifests and active-selection events at startup or when you choose **Refresh from R2**. Background sync uploads local changes without listing the bucket, minimizing Class A operations while allowing a fresh device to recover remote history on those refresh boundaries.

See [`IDEA.MD`](IDEA.MD) for the complete product direction.
