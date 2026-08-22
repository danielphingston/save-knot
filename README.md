# SaveKnot

SaveKnot is a tiny, local-first game-save daemon. It discovers Steam games through the [Ludusavi manifest](https://github.com/mtkennerly/ludusavi-manifest), watches their save locations, creates immutable content-addressed snapshots, and sends them directly to your own Cloudflare R2 bucket.

There is no SaveKnot account, hosted backend, central database, analytics service, or remote control plane.

## What works

- Ludusavi manifest download with ETag caching and validation before activation
- Steam library and installed-game discovery
- Manual games and arbitrary absolute save paths
- Filesystem notifications with a three-second debounce and a maximum delay
- Stable-read checks to avoid half-written game saves
- SHA-256 content addressing and zstd compression
- Immutable local snapshot history in SQLite
- Direct, bucket-scoped R2 uploads; blobs publish before snapshot manifests
- Conservative restores with a pre-restore snapshot and hash verification
- Custom titles, notes, enable/disable state, extra save locations, and uploaded artwork
- Embedded localhost UI and server-sent activity events
- OS credential-vault storage for the R2 secret access key

Windows registry saves and non-Steam store discovery are intentionally not claimed as supported yet. Catalog entries containing registry data are retained by the parser, but this release only snapshots files.

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

SaveKnot deliberately rejects non-loopback listen addresses.

## Connect R2

1. Create an R2 bucket in Cloudflare.
2. Create an S3 API token with **Object Read & Write** access scoped to that bucket.
3. Open **Settings** in SaveKnot.
4. Enter the account ID, bucket, access key ID, secret access key, and optional object prefix.
5. Select **Test & connect**. Settings are only committed after `HeadBucket` succeeds.

The secret is stored with Windows Credential Manager, macOS Keychain, or the Linux Secret Service through the OS keyring. Non-secret connection metadata lives in `config.json` with user-only permissions.

R2 objects use this layout:

```text
<prefix>/v1/
├── blobs/sha256/<first-two-hash-characters>/<sha256>.zst
└── games/<game-id>/snapshots/<snapshot-id>.json
```

Blobs are immutable by their SHA-256 name. A snapshot becomes visible remotely only after all of its blobs have uploaded successfully.

## Architecture

SaveKnot borrows Cordis's strongest architectural idea: runtime capabilities have explicit owners and lifetimes. It does not reproduce Cordis's dynamic context, proxy, or service-location machinery.

```text
Application lifecycle
├── catalog + Steam discovery coordinator
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

Install `golangci-lint` v2.12 or newer, then run:

```sh
make check
```

The gate runs the curated correctness, error-handling, context, resource, complexity, hygiene, and security linters from [`GO_AI_CODE_QUALITY.md`](GO_AI_CODE_QUALITY.md), plus normal and race tests. CI also verifies formatting, coverage reporting, `go mod tidy`, and clean diffs.

Dependencies are intentionally narrow and each owns a boundary the standard library does not cover:

- AWS SDK for Go v2: supported R2/S3 protocol client
- modernc SQLite: embedded, CGO-free state database
- fsnotify: cross-platform filesystem notifications
- yaml.v3: Ludusavi manifest decoding
- doublestar: Ludusavi-compatible recursive glob expansion
- klauspost/compress: zstd blob encoding
- go-keyring: native operating-system credential storage

## Current scope and tradeoffs

- The embedded UI uses plain HTML, CSS, and JavaScript. This keeps the release to one Go build and avoids a Node production toolchain; a framework can be introduced when UI complexity justifies it.
- Discovery is Steam-first to avoid expanding glob trees for every catalog entry. Manual games cover unsupported stores today.
- Snapshot manifests reference logical source keys, not absolute catalog paths. A restore requires the corresponding save location to be configured on that device.
- Automatic launch-at-login packaging is operating-system-specific and is not installed implicitly by `go run`; release installers should add a per-user startup entry.

See [`IDEA.MD`](IDEA.MD) for the complete product direction.
