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
- Embedded localhost UI, native folder picker, discovery diagnostics, and server-sent activity events
- Persistent rotating logs at `<data directory>/logs/saveknot.log`
- OS credential-vault storage for the R2 secret access key

## Run it

Requirements: Go 1.26 or newer.

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

The Windows release is linked as a GUI/background executable, so it does not leave a console window open. Run it, then open <http://127.0.0.1:32147>. For troubleshooting, inspect `%APPDATA%\SaveKnot\logs\saveknot.log`; use `-log-level debug` from PowerShell for more detail.

SaveKnot deliberately rejects non-loopback listen addresses.

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
└── games/<game-id>/snapshots/<snapshot-id>.json
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

The script resolves and installs the latest official stable Go toolchain with checksum verification, rejects versions older than `go.mod`, clears stale `GOROOT` state from the preinstalled runtime, verifies that the compiler matches the selected Go release, installs the system compiler needed by `go test -race`, prefetches module dependencies, and installs the pinned `golangci-lint` release while setup networking is available. It also installs the latest `agent-browser` CLI, Chromium runtime, Linux browser dependencies, and bundled core browser skill so cloud agents can perform UI verification. It is safe to run again when Codex resumes a cached environment. Set `SAVEKNOT_GO_VERSION` or `SAVEKNOT_AGENT_BROWSER_VERSION` in the environment settings only when you intentionally want to pin a specific release. If the script body must be pasted directly, it can locate the checkout from the current working directory or `SAVEKNOT_REPO_ROOT`.

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

- The embedded UI uses plain HTML, CSS, and JavaScript. This keeps the release to one Go build and avoids a Node production toolchain; a framework can be introduced when UI complexity justifies it.
- Discovery checks store manifests first. The deeper catalog scan is bounded and only evaluates user-anchored rules whose literal parent directory exists; install-root rules are not expanded blindly across the full catalog.
- Snapshot manifests reference logical source keys, not absolute catalog paths. A restore requires the corresponding save location to be configured on that device.
- R2 reconciliation intentionally lists snapshot manifests only at startup/manual boundaries. This minimizes Class A operations while still allowing a fresh device to recover remote history.

See [`IDEA.MD`](IDEA.MD) for the complete product direction.
