The project should be a tiny per-user background daemon that consumes the Ludusavi Manifest as a catalog, discovers installed games, watches their save locations, creates immutable snapshots, and syncs those snapshots directly to the user's own Cloudflare R2 bucket. No account system, no hosted backend, no central database.

For the name, my favorite is **SaveKnot** — it fits the idea of tying local saves, machines, snapshots, and cloud storage together without sounding like a Ludusavi wrapper. Other decent options are **Waymark**, **Reprise**, **SaveRelay**, and **Checkpoint**, though the latter two are much more generic. I would avoid **SaveWard** because that name has already been used by an older save-management project. ([npm][1])

# SaveKnot

## 1. Product model

The basic architecture should be:

```text
             ┌──────────────────────────┐
             │      Local Web UI        │
             │  localhost:port          │
             └────────────┬─────────────┘
                          │
                    Local HTTP API
                          │
             ┌────────────▼─────────────┐
             │       SaveKnot Core      │
             │                          │
             │ catalog                  │
             │ discovery                │
             │ watcher                  │
             │ snapshot engine          │
             │ restore engine           │
             │ metadata                 │
             │ scheduler                │
             │ storage                  │
             └──────┬────────────┬──────┘
                    │            │
             local filesystem   R2
                    │            │
          Ludusavi Manifest      │
          game/store metadata    │
                                 ▼
                         User's R2 bucket
```

There is deliberately **no SaveKnot server**.

The only external services involved are:

* Ludusavi Manifest download
* the user's R2 bucket
* optional artwork providers later

R2 is S3-compatible, and Cloudflare explicitly supports the AWS SDK for Go, so the daemon can communicate with R2 directly. ([Cloudflare Docs][2])

We don't need presigned URLs either. Those would mainly make sense if a remote browser were uploading directly to R2. Our browser talks to the local daemon and the daemon owns the R2 connection. ([Cloudflare Docs][3])

---

# 2. Go vs Rust

With embedding Ludusavi removed:

| Concern                   | Go              | Rust            |
| ------------------------- | --------------- | --------------- |
| Background daemon         | **Excellent**   | Excellent       |
| Filesystem watching       | **Excellent**   | Excellent       |
| R2/S3 libraries           | **Excellent**   | Excellent       |
| SQLite                    | Excellent       | Excellent       |
| Concurrency               | **Very simple** | More involved   |
| Cross-compiling           | **Simple**      | Good            |
| Development speed         | **Higher**      | Lower           |
| Idle memory               | Good            | **Best**        |
| Binary startup            | Excellent       | Excellent       |
| Native web UI backend     | **Very easy**   | Easy            |
| Long-term maintenance     | **Simpler**     | More complexity |
| Safety for low-level code | Good            | **Excellent**   |
| Benefit to this workload  | **High**        | Limited         |

### Pick: Go.

Rust would probably produce a slightly leaner daemon, but that's optimizing something that is already tiny.

Our actual workload is:

```text
sleep
↓
filesystem event
↓
hash a few files
↓
compress
↓
upload
↓
sleep
```

Go handles this extremely well.

It also gives us a clean single-process architecture:

```text
saveknot.exe
 ├─ filesystem watchers
 ├─ SQLite
 ├─ sync workers
 ├─ embedded frontend
 └─ localhost API
```

No Node runtime, no Electron, no sidecar.

### One thing I would specifically NOT use

Go's native `plugin` package.

It's awkward across platforms, particularly Windows, and tightly couples plugin binaries to Go/compiler versions.

We'll build a plugin-shaped architecture, but initially compile modules directly into SaveKnot. If external plugins become useful, use a stable process/WASM protocol instead.

---

# 3. What we should copy from Cordis

Cordis itself describes the project as a composability framework, and its repository is decomposed into packages such as `core`, `loader`, `timer`, `group`, and utilities.

More importantly, its `Context` owns services such as the event system, logger, registry and reflection system.

Its `Fiber` system then gives modules/plugins explicit lifecycle state and tracks resources/disposables so they can be cleanly loaded and unloaded.

That's the part I would copy conceptually.

Not this:

```text
giant SaveManager
 ├─ downloads manifest
 ├─ finds games
 ├─ watches files
 ├─ calls R2
 ├─ owns UI
 ├─ restores
 └─ manages config
```

Instead:

```text
App
│
├── CatalogService
├── DiscoveryService
├── WatchService
├── SnapshotService
├── StorageService
├── RestoreService
├── MetadataService
├── EventBus
└── Scheduler
```

Modules register against core interfaces and have a lifecycle:

```text
Init
 ↓
Start
 ↓
Running
 ↓
Stop
```

But I would keep it idiomatic Go rather than reproduce Cordis's runtime service locator/proxy system.

Use compile-time interfaces and constructor injection wherever possible.

---

# 4. Repository structure

Something like:

```text
saveknot/
│
├── cmd/
│   └── saveknot/
│       └── main.go
│
├── internal/
│   ├── app/
│   ├── events/
│   ├── config/
│   ├── database/
│   │
│   ├── catalog/
│   │   └── ludusavi/
│   │
│   ├── discovery/
│   │   ├── steam/
│   │   ├── epic/
│   │   ├── gog/
│   │   └── custom/
│   │
│   ├── watcher/
│   ├── snapshot/
│   ├── restore/
│   ├── storage/
│   │   └── r2/
│   │
│   ├── metadata/
│   ├── artwork/
│   ├── scheduler/
│   └── api/
│
├── web/
│   ├── src/
│   └── ...
│
├── migrations/
│
└── docs/
```

Later:

```text
plugins/
├── artwork-steam/
├── artwork-steamgriddb/
├── storage-s3/
├── storage-b2/
├── detector-battlenet/
└── metadata-...
```

The initial implementations are compiled-in modules.

---

# 5. Ludusavi integration

We consume the **Ludusavi Manifest**, nothing else.

That's actually explicitly supported by the project: the manifest format is intended to be generic enough for other game backup tools to implement. ([GitHub][4])

It already gives us:

```yaml
Game:
  files:
  registry:
  installDir:
  steam:
  gog:
  id:
  aliases:
  when:
  tags:
```

And placeholders such as:

```text
<home>
<root>
<base>
<game>
<storeGameId>
<storeUserId>
<winAppData>
<winLocalAppData>
<winDocuments>
...
```

The manifest specifically requires implementations to substitute those placeholders and then treat the resulting paths as globs. ([GitHub][4])

### Manifest updating

Ludusavi recommends ETag-based updates:

```text
GET manifest

ETag: abc
      ↓
save locally

next refresh:

If-None-Match: abc
      ↓
304 = unchanged
200 = update cache
```

So that's exactly what we should do. ([GitHub][4])

No need to hit GitHub constantly.

---

# 6. Don't scan 19,000 games blindly

This is an important implementation detail.

Ludusavi supports more than 19,000 games. ([GitHub][5])

We shouldn't evaluate thousands of glob trees every few seconds.

Instead:

```text
STORE DETECTOR
      ↓
Installed game IDs
      ↓
Ludusavi catalog lookup
      ↓
20-100 relevant entries
      ↓
Resolve save locations
```

For Steam:

```text
Steam libraries
    ↓
appmanifest_*.acf
    ↓
AppID
    ↓
manifest.steam.id
    ↓
game
```

Then resolve the game's save patterns.

Do similar store adapters later.

Initial priority:

```text
1. Steam
2. manually configured games/root
3. Epic
4. GOG
5. others
```

This keeps discovery cheap.

---

# 7. Save discovery overrides

A detected game gets a canonical entry:

```text
Catalog definition
        +
Local overrides
        =
Effective definition
```

For example:

```text
Cyberpunk 2077

Catalog paths
✓ AppData/...
✓ user profile/...

Overrides
+ D:/mods/cyberpunk/save-data
- settings.json
```

Users should be able to:

* rename game
* override image
* add save directories
* remove catalog directories
* exclude specific files
* disable syncing
* change debounce policy
* trigger backup
* restore snapshot
* remap the game to another catalog entry

This means bad Ludusavi data doesn't block the user.

---

# 8. Local database

SQLite.

No reason for anything else.

Rough schema:

```text
games
-----
id
catalog_id
catalog_name
display_name
store
store_id
installed_path
enabled
last_seen
last_backup
last_change


game_paths
----------
id
game_id
source
path_template
resolved_path
enabled


game_metadata
-------------
game_id
custom_name
custom_image
notes


snapshots
---------
id
game_id
device_id
created_at
file_count
original_size
stored_size
remote_state


snapshot_files
--------------
snapshot_id
logical_path
blob_hash
size
mtime


blobs
-----
hash
size
compressed_size
uploaded


sync_jobs
---------
id
game_id
status
created_at
started_at
completed_at
error
```

SQLite is a cache/state database.

**R2 remains the actual backup source of truth.**

---

# 9. R2 storage model

I would not simply upload:

```text
Cyberpunk2077/save.dat
```

and overwrite it forever.

That's a sync system that eventually destroys something.

Instead, use immutable snapshots.

Something like:

```text
saveknot/
└── v1/
    ├── blobs/
    │   └── sha256/
    │       ├── 17/
    │       │   └── 17ab923...
    │       └── af/
    │           └── af81c21...
    │
    ├── games/
    │   └── cyberpunk-2077/
    │       ├── metadata.json
    │       └── snapshots/
    │           ├── 01991....json
    │           ├── 01992....json
    │           └── 01993....json
    │
    └── devices/
        └── desktop-abc.json
```

### Blob storage

Files become:

```text
SHA256(file contents)
         ↓
Zstd
         ↓
blobs/sha256/<hash>
```

Snapshots merely reference them.

Example:

```json
{
  "id": "0199...",
  "game": "cyberpunk-2077",
  "device": "desktop-a",
  "created": "...",
  "files": [
    {
      "path": "save/ManualSave-1.dat",
      "hash": "7fb..."
    },
    {
      "path": "save/profile.dat",
      "hash": "28a..."
    }
  ]
}
```

If only one 5 KB save changes:

```text
old snapshot:
A B C D

new snapshot:
A B C E
```

Only `E` has to be uploaded.

That's much better than uploading a complete ZIP every time.

---

# 10. Why immutable snapshots matter

It solves several problems simultaneously:

### Version history

```text
11:21
11:34
12:07
14:42
```

Any version can be restored.

### Two computers

Desktop:

```text
snapshot D1
```

Laptop:

```text
snapshot L1
```

Neither destroys the other.

### Interrupted uploads

Incomplete upload:

```text
blobs uploaded
snapshot manifest missing
```

No valid snapshot exists yet.

Therefore the backup remains consistent.

### Corrupted local save

The corrupted version creates another snapshot.

It doesn't overwrite the last good version.

That should be one of SaveKnot's core design rules:

> **Backups are immutable.**

---

# 11. File watcher

The background process shouldn't poll everything.

Use filesystem notifications.

Conceptually:

```text
save path
   ↓
fsnotify
   ↓
change event
   ↓
debounce
   ↓
stable-file check
   ↓
snapshot
   ↓
R2
```

Don't snapshot immediately after the first write.

Games often do:

```text
write temp
rename
write
rename
write metadata
```

So:

```text
change
   ↓
wait ~3 sec
   ↓
another change?
 yes → reset timer
 no
   ↓
snapshot
```

And before hashing:

```text
stat
read
stat

size/mtime changed?
    ↓
 retry
```

This avoids backing up half-written saves.

We should also have a maximum debounce so a game that constantly writes doesn't prevent backup forever.

---

# 12. Watch non-existent paths

Another subtle issue.

A manifest might say:

```text
AppData/MyGame/Saves
```

but the user hasn't launched the game yet.

We can't watch a directory that doesn't exist.

So watch the nearest existing parent:

```text
AppData/
   ↓
MyGame appears
   ↓
attach watcher
   ↓
Saves appears
   ↓
attach watcher
```

And periodically reconcile watchers.

That's more reliable than relying entirely on FS events.

---

# 13. Restore behavior

Restore should be intentionally conservative.

When clicking:

```text
Restore
```

SaveKnot should:

```text
1. snapshot existing local state
2. download requested snapshot
3. validate hashes
4. write temporary files
5. atomically replace originals
6. verify
```

Never:

```text
delete current save
download remote
hope everything works
```

The pre-restore snapshot gives us an immediate undo.

---

# 14. Registry saves

The Ludusavi catalog also contains Windows registry entries, not just files. ([GitHub][4])

I would model them immediately even if implementation lands slightly later:

```text
SaveSource
├── FileSource
└── RegistrySource
```

The UI can then correctly say:

```text
Game backup coverage

Files       ✓
Registry    ⚠ not supported yet
```

rather than silently pretending the backup is complete.

Before calling v1.0 "reliable," registry backup/restore should work.

---

# 15. UI

The UI should be deliberately boring.

Something like:

```text
┌────────────────────────────────────────────────────┐
│ SaveKnot                              ● Synced      │
├────────────────────────────────────────────────────┤
│ Games      Activity      Storage      Settings     │
├────────────────────────────────────────────────────┤
│                                                    │
│  ┌────────┐ Cyberpunk 2077                         │
│  │ cover  │ Last backup: 2 min ago                 │
│  │        │ 14 snapshots • 82 MB                   │
│  └────────┘ ● Synced                               │
│                                                    │
│  ┌────────┐ Baldur's Gate 3                        │
│  │ cover  │ Last backup: 14 min ago                │
│  │        │ 31 snapshots • 144 MB                  │
│  └────────┘ ● Synced                               │
│                                                    │
└────────────────────────────────────────────────────┘
```

Click game:

```text
Cyberpunk 2077
──────────────────────────────

[ image ]

Synced                 ✓
Last changed          14:38
Last backup           14:38
Remote snapshots      14
Storage               82 MB

BACKUPS

Today 14:38       5 files       4.2 MB
Today 11:02       5 files       4.2 MB
Yesterday 22:41   5 files       4.1 MB

[Restore] [Backup now]

Save locations
─────────────────────
✓ AppData/...
✓ Documents/...

+ Add location
```

That's enough.

---

# 16. Artwork

Make artwork its own provider interface:

```text
ArtworkProvider
```

Initial fallback order:

```text
custom image
     ↓
store artwork if easily available
     ↓
executable icon
     ↓
generated placeholder
```

Critically, **custom images always win**.

Portable metadata like:

```text
name
picture
tags
notes
```

can eventually be synced to R2.

Machine-specific configuration such as:

```text
D:\Games\...
C:\Users\Daniel\...
```

should not.

Separate them from day one:

```text
PortableGameMetadata

DeviceGameConfig
```

That prevents multi-PC configuration from becoming messy later.

---

# 17. R2 setup

First-run screen:

```text
Connect Cloudflare R2

Account ID
[________________]

Bucket
[________________]

Access Key ID
[________________]

Secret Access Key
[________________]

Prefix
[saveknot________]

[Test Connection]

              Connect
```

Endpoint automatically becomes approximately:

```text
https://<ACCOUNT_ID>.r2.cloudflarestorage.com
```

Cloudflare's S3 examples use that endpoint format. ([Cloudflare Docs][3])

Credentials should go into the OS credential/key store, not plaintext SQLite.

SQLite only stores something like:

```text
provider = r2
bucket = gamesaves
account_id = ...
credential_ref = "saveknot-r2-default"
```

---

# 18. Local UI technology

I'd use:

```text
Go
+
plain HTML/CSS/JavaScript
+
embedded static assets
```

Keep the browser files directly in:

```text
internal/api/web/
```

Then:

```go
//go:embed web/index.html web/styles.css web/app.js web/helpers.mjs
```

This keeps the local UI small and removes the frontend framework, bundler, and
package-install step from normal builds.

The binary serves it at:

```text
127.0.0.1:<port>
```

The browser frontend communicates through:

```text
/api/games
/api/snapshots
/api/settings
/api/storage
/api/restore
```

and SSE/WebSocket:

```text
/events
```

for:

```text
backup started
file changed
upload progress
backup complete
restore complete
error
```

The browser being closed changes nothing.

The Go daemon keeps working.

---

# 19. Don't use a Windows Service

For Windows specifically, I would run SaveKnot as a **per-user startup process**, not a SYSTEM service.

We need easy access to:

```text
%APPDATA%
%LOCALAPPDATA%
Documents
Steam user data
registry HKCU
```

A Windows service complicates all of that.

Instead:

```text
user logs in
    ↓
SaveKnot starts quietly
    ↓
background process
```

Linux can use:

```text
systemd --user
```

Mac:

```text
LaunchAgent
```

Same model.

---

# 20. Core interfaces

This is where we make the Cordis-style modularity useful.

Something around:

```go
type Catalog interface {
    Games(ctx context.Context) ([]GameDefinition, error)
    Find(ctx context.Context, id GameID) (*GameDefinition, error)
}

type Detector interface {
    Discover(ctx context.Context) ([]InstalledGame, error)
}

type Storage interface {
    PutBlob(ctx context.Context, blob Blob) error
    GetBlob(ctx context.Context, hash Hash) (io.ReadCloser, error)

    PutSnapshot(ctx context.Context, snapshot Snapshot) error
    ListSnapshots(ctx context.Context, game GameID) ([]Snapshot, error)
}

type Snapshotter interface {
    Snapshot(ctx context.Context, game Game) (*Snapshot, error)
}

type ArtworkProvider interface {
    Artwork(ctx context.Context, game Game) (*Artwork, error)
}
```

Then:

```text
Catalog
 └ LudusaviManifest

Storage
 └ R2

Detector
 ├ Steam
 ├ Epic
 └ Custom

Artwork
 ├ Custom
 ├ Steam
 └ Executable
```

Adding functionality becomes composition rather than rewrites.

---

# 21. Event model

A central typed event bus will be useful.

Events:

```text
CatalogUpdated

GameDiscovered
GameRemoved

SaveChanged
SaveSettled

SnapshotStarted
SnapshotCompleted
SnapshotFailed

UploadStarted
UploadProgress
UploadCompleted

RestoreStarted
RestoreCompleted

StorageConnected
StorageError
```

Modules subscribe without knowing about one another.

For example:

```text
Watcher
   │ SaveSettled
   ▼
Snapshotter
   │ SnapshotCompleted
   ▼
SyncService
   │
   ▼
R2
```

The UI subscribes to the same events.

Very Cordis-like conceptually, without implementing Cordis itself.

---

# 22. Plugin strategy

### V1

No external plugins.

Just internal modules.

```text
internal/modules/...
```

### Later

If someone wants:

```text
Dropbox
Google Drive
Backblaze B2
OneDrive
SteamGridDB
custom detector
custom save parser
```

then expose a plugin protocol.

I'd probably use:

```text
SaveKnot
   ↕ stdio JSON-RPC
Plugin process
```

rather than Go ABI plugins.

Then plugins can be written in:

```text
Go
Rust
Python
Node
anything
```

without ABI coupling.

WASM would also be worth considering for simple metadata/detector plugins.

---

# 23. Development phases

### Phase 0 — foundation

Build:

```text
Go application
module lifecycle
event bus
SQLite
config
logging
embedded UI
```

No sync yet.

### Phase 1 — catalog

Implement:

```text
Ludusavi manifest downloader
ETag caching
YAML parsing
schema model
placeholder resolver
```

UI:

```text
Catalog: 19k+ games
Last updated ...
```

### Phase 2 — Steam discovery

Implement:

```text
Steam root detection
libraryfolders parsing
appmanifest parsing
AppID matching
save path resolution
```

Now UI can show:

```text
Installed games
Detected save paths
```

### Phase 3 — snapshots

Implement:

```text
filesystem scanning
SHA256
zstd
content-addressed blobs
snapshot manifest
SQLite state
```

Local-only first.

### Phase 4 — R2

Implement:

```text
credentials
connection test
bucket/prefix validation
blob upload
snapshot upload
snapshot listing
download
```

At this point the core product works.

### Phase 5 — automatic sync

Implement:

```text
fsnotify
watch manager
debounce
stable-read verification
background queue
retry/backoff
offline handling
```

Now it's genuinely set-and-forget.

### Phase 6 — restore/history

Implement:

```text
snapshot browser
diff
restore
automatic pre-restore snapshot
integrity verification
```

### Phase 7 — customization

Implement:

```text
custom names
custom artwork
custom save paths
excluded paths
manual games
sync toggles
```

### Phase 8 — completeness

Add:

```text
Windows registry backup
Epic detection
GOG detection
portable metadata syncing
multi-device handling
retention rules
```

Then call it v1.

---

# 24. MVP I would actually build

I wouldn't attempt every store immediately.

Our first useful version should be exactly:

```text
SaveKnot v0.1

✓ Windows
✓ Go daemon
✓ local browser UI
✓ SQLite
✓ Ludusavi Manifest
✓ Steam discovery
✓ custom games
✓ custom save paths
✓ filesystem watcher
✓ automatic snapshots
✓ snapshot history
✓ SHA256 deduplication
✓ zstd compression
✓ R2
✓ restore
✓ custom title
✓ custom picture
✓ launch at login
```

And explicitly postpone:

```text
× accounts
× SaveKnot cloud
× sharing
× mobile app
× external plugins
× SteamGridDB API
× every game store
× fancy synchronization protocol
× public remote UI
```

That is small enough to remain good.

---

## The most important architectural choice

I'd treat this as a **snapshot system that automatically synchronizes**, not as a folder synchronization application.

That's the distinction that gives us:

```text
history
deduplication
multiple PCs
safe restores
corruption protection
atomic backups
```

without having to solve Dropbox-style bidirectional file synchronization.

So the core mental model becomes:

```text
             SAVEKNOT

filesystem change
       │
       ▼
 immutable snapshot
       │
       ▼
 content-addressed objects
       │
       ▼
       R2
       │
       ▼
 snapshot history
```

And then Cordis's architectural idea maps nicely onto the implementation: **small independently owned services with explicit lifecycle and an event-driven core**, rather than one giant backup manager. Cordis itself separates services and plugin lifecycle in roughly this manner; its service abstraction registers capabilities against a context, while Fibers manage runtime lifetime and cleanup.

**SaveKnot + Go + local web UI + immutable R2 snapshots + Ludusavi Manifest as a pure catalog** is the direction I'd build.

[1]: https://www.npmjs.com/package/%40save-ward/save-parser-skyrim?utm_source=chatgpt.com "@save-ward/save-parser-skyrim - npm"
[2]: https://developers.cloudflare.com/r2/api/s3/?utm_source=chatgpt.com "S3 · Cloudflare R2 docs"
[3]: https://developers.cloudflare.com/r2/api/s3/presigned-urls/?utm_source=chatgpt.com "Presigned URLs · Cloudflare R2 docs"
[4]: https://github.com/mtkennerly/ludusavi-manifest?utm_source=chatgpt.com "GitHub - mtkennerly/ludusavi-manifest: Backup info for PC game saves · GitHub"
[5]: https://github.com/mtkennerly/ludusavi?utm_source=chatgpt.com "GitHub - mtkennerly/ludusavi: Backup tool for PC game saves · GitHub"
