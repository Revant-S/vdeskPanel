# Vault: Encrypted Drive-Backed Storage, v1 Spec

Oct 20, 2026 · @Cosmoboy

## 1. Overview

Vault is a Linux tool, written in Go, that turns a 5 TB Google Drive into encrypted storage that behaves like a normal local folder. A background daemon mounts a folder (default `~/Vault`) with FUSE. Every file and folder created there is encrypted on the laptop and mirrored to Drive with the same nesting. File contents live in the cloud and are downloaded only when opened, so the laptop's disk stops filling up. A small control panel shows the vault's tree, sync status, a git-style history of every change from every device, and the trash.

The main reason it exists is protection against losing the laptop. The owner's current laptop could fail at any time. Working in `~/Vault` must mean that, a few seconds after any change, the data is safe on Drive, and a fresh laptop can rebuild the whole vault from the password and the Google account alone. Saving local disk space is a secondary benefit.

Goals for v1:

1. No data loss when the laptop dies: every change is uploaded and verified within seconds, and a new laptop can restore the full vault. The mount works with every Linux app: Nautilus, VLC, LibreOffice, VS Code, `cp`, `mv`, shells.
2. Google never sees file contents or names. The password never leaves the machine and is never written to disk.
3. A second laptop can attach to the same vault with the password and browse everything without downloading anything up front.
4. Every create, edit, rename, move and delete is recorded with the device that made it, and shown as a graph.
5. Deleted files sit in an encrypted trash on Drive for 30 days and can be restored or purged from the panel.
6. Sync is fast and parallel but stays well inside Drive's API limits.

Out of scope for v1: Windows and macOS, version history and undo (beyond the trash), sharing with other people, password change, selective sync rules, mobile clients.

## 2. Decisions and assumptions

These are fixed for v1. Rows marked *default* were not specified by the owner and can be changed before the build starts.

| Topic | Decision | Origin |
| --- | --- | --- |
| Interface | FUSE mount at `~/Vault` plus a separate control panel window | Owner |
| Drive layout | Folder tree on Drive mirrors the vault exactly; every name and every file body is encrypted | Owner |
| Browsing | Files are browsed through the mount and the panel. Drive's web UI shows only encrypted names | Owner |
| Password | Argon2id-derived key held only in daemon RAM; asked on every start; never on disk, never in a keyring | Owner |
| Google sign-in | OAuth in the browser; the refresh token is stored on disk encrypted with the vault key | Default |
| Local copies | The device keeps a full local copy of the vault by default. Files are always on disk and always backed up | Owner |
| Freeing space | Panel and right-click action "Free up space" drops local copies of chosen files or folders. Space is secondary; nothing is evicted automatically | Default |
| Conflicts | Last write wins by hybrid logical clock; the losing version goes to trash; the log names the device | Owner |
| Moved out of vault | Treated as a delete: goes to trash, logged, desktop notification | Owner |
| Moved back in | Treated as a new file; no restore from trash; identical content is relinked instead of re-uploaded | Owner |
| History | Read-only graph; no undo or restore in v1 | Owner |
| Trash | Encrypted trash folder on Drive, kept 30 days; restore and purge from the panel | Owner |
| Language | Go | Owner |
| Panel toolkit | Wails v2 with a plain TypeScript and SVG frontend | Default |
| Platform | Linux only (Ubuntu first) | Owner |
| Content | Mostly media and documents, so large files stream and resume | Owner |
| Drive scope | `drive.file` only; the app cannot see the rest of the Drive | Default |
| Recovery key | None. A lost password means the data is gone | Default |

Priority order when two goals conflict: durability first, then correctness of the mirror, then speed, then local disk space. Section 12 covers durability and recovery.

## 3. Architecture

One daemon, `vaultd`, does all the work: it serves the mount, holds the keys, keeps the index and cache, and talks to Drive. The panel and CLI are thin clients over a local socket.

&#91;embedded content: vaultd components and what talks to them\]

Apps see a normal folder through FUSE; the panel and CLI only send requests to the daemon's API; everything crossing to Drive has already been encrypted.

- `vaultd` runs as a systemd user service, starts at login, and stays locked until the password arrives.
- The panel can be closed at any time; syncing and notifications continue.
- One vault per user account per machine in v1.

## 4. Security model and crypto

Google and anyone holding the Drive account see ciphertext only. The laptop's disk holds the same ciphertext as Drive, so a stolen laptop without the password reveals no names or contents either.

What Google can still see, by design of the mirrored layout: the folder tree's shape, the number of entries per folder, approximate file sizes, and modification times. This is accepted for v1.

### Primitives

Use only `golang.org/x/crypto` and the standard library. No custom primitives.

| Purpose | Algorithm | Parameters |
| --- | --- | --- |
| Password to key-encryption key | Argon2id | 3 passes, 256 MiB memory, 4 lanes, 32-byte random salt |
| Key wrapping, names, segments | XChaCha20-Poly1305 | 24-byte random nonce per encryption |
| Subkey derivation | HKDF-SHA256 | from the vault master key, one info label per purpose |
| Content hashing | SHA-256 over plaintext | used for dedup and change detection, stored encrypted |

### Key hierarchy

1. On vault creation the daemon generates a random 32-byte vault master key (VMK) and a random 16-byte vault ID.
2. The password goes through Argon2id with the stored salt to produce a key-encryption key. That key wraps the VMK with the vault ID as associated data.
3. The `vault.header` file on Drive holds: format version, vault ID, Argon2id parameters, salt, wrapped VMK. It contains no secret in the clear. A wrong password fails the unwrap, which is the password check.
4. HKDF derives separate subkeys from the VMK: `name`, `content`, `log`, `token`, `index`.
5. Each file version gets its own random 32-byte file key, wrapped with the `content` subkey and stored in that file's header.

### File body format

A file body is a header followed by fixed segments, so any byte range can be read or rewritten without touching the rest.

- Header: magic `VLT1`, format version, file ID, wrapped file key.
- Segment: 24-byte nonce, then 64 KiB of plaintext encrypted, then the 16-byte tag. The last segment may be shorter.
- Associated data per segment: file ID, segment index, and a final-segment flag. This stops reordering, swapping between files and truncation.
- Every rewrite of a segment uses a fresh random nonce.
- Plaintext offset `p` lives in segment `p / 65536`; its ciphertext starts at `header + index × (65536 + 40)`.

### Names

Each file and folder name is encrypted with the `name` subkey and a random nonce, then base64url-encoded without padding. That string is the object's name on Drive and in the local index. A rename re-encrypts the name; a move only changes the Drive parent.

### Password handling

1. The panel collects the password and sends it to the daemon over the local socket, then zeroes its buffer.
2. The daemon derives keys, zeroes the password, and keeps only the subkeys.
3. Key memory is locked with `mlock`, the process sets `PR_SET_DUMPABLE` to 0 and `RLIMIT_CORE` to 0, so keys never reach swap or a core dump.
4. Lock (panel button, or daemon stop) unmounts the vault and wipes every key buffer.
5. Nothing derived from the password is ever written to disk or to the system keyring.

### Google credentials

Sign-in is standard OAuth 2.0 for desktop apps with PKCE and a loopback redirect, opened in the default browser. The app never sees the Google password. The refresh token is encrypted with the `token` subkey and stored at `~/.local/share/vault/token.enc`, so Drive access needs the vault password too.

The owner creates a Google Cloud project, enables the Drive API and makes a Desktop OAuth client. The scope is `drive.file`, a non-sensitive scope, which lets the app see only files it created. The consent screen must be set to **In production**: in Testing status Google expires refresh tokens after 7 days.

## 5. Drive layout

Everything the app owns lives under one Drive folder, and the user's tree under `tree/` mirrors `~/Vault` one to one, with encrypted names.

```
Vault/                          plaintext name, created by the app
  vault.header                  KDF params, salt, wrapped master key
  tree/                         mirror of ~/Vault
    Xk3p9...Qa/                 an encrypted folder name
      7Fhs2...pL                an encrypted file name, body in VLT1 format
  log/
    <device-id>/
      <commit-id>.vlog          one encrypted commit per sync batch
  devices/
    <device-id>.vdev            encrypted device record: name, first seen
  trash/
    <trash-id>                  a deleted file or a whole deleted folder
```

Rules:

- Every Drive object carries `appProperties`: `vid` (vault ID) and `oid` (a random object ID that never changes across renames and moves). The object ID links Drive files, local index rows and log entries without decrypting anything.
- Drive's `modifiedTime` is set from the local mtime on upload.
- A rename is a Drive metadata update of the name. A move is a parent change. Neither re-uploads content.
- A delete moves the object into `trash/` under a new random name. Deleting a folder moves the folder once; its subtree goes with it.
- The app never reads, lists or writes anything outside its own `Vault/` folder. With the `drive.file` scope it cannot.
- Format versions are checked on every unlock. A newer format than the running build refuses to mount and says so in the panel.

## 6. Local storage and cache

The laptop keeps a metadata index for the whole tree and file bodies only for what has been opened, all in the same encrypted form as on Drive.

| Path | Contents |
| --- | --- |
| `~/.config/vault/config.toml` | mount point, device name, Drive folder ID, OAuth client file path, worker counts |
| `~/.local/share/vault/index.db` | SQLite in WAL mode, via `modernc.org/sqlite` (pure Go, no cgo) |
| `~/.local/share/vault/cache/<oid[0:2]>/<oid>` | file bodies in VLT1 format, sparse when partly downloaded |
| `~/.local/share/vault/token.enc` | OAuth refresh token, encrypted |
| `~/.local/state/vault/daemon.log` | operational log for debugging, no names or contents |

### Index tables

- `objects`: object ID, parent object ID, kind (file or folder), encrypted name, Drive ID, plaintext size, mtime, encrypted content hash, remote version, local state.
- `segments`: object ID and a bitmap of which 64 KiB segments are present locally.
- `pending_ops`: the outgoing queue. Operation, object ID, payload, attempt count, next attempt time. Survives restarts.
- `commits`: every log commit seen, local or remote, with its parents and whether it is applied.
- `trash`: trash ID, original object ID, encrypted original path, deleted time, device, reason.
- `kv`: the Drive changes page token, the device's clock state, schema version.

At unlock the daemon loads `objects` and decrypts all names in parallel into an in-memory tree. All filesystem lookups are served from that tree; SQLite is the durable copy.

### File states

| State | Meaning | Shown in panel as |
| --- | --- | --- |
| Cloud only | metadata only, no body on disk | cloud icon |
| Partial | some segments on disk, for example a video being streamed | half-filled icon |
| Local | full body on disk | filled icon |
| Dirty | local changes not yet uploaded | sync arrow |

### Behaviour

1. Opening a cloud-only file fetches the segments that are read, in runs of 8 MiB via HTTP Range requests, and prefetches ahead on sequential reads.
2. Once a file is fully read, or opened by an app that reads it all, it becomes Local and stays Local.
3. When a Local file changes on another device, the new version downloads in the background into a new cache path. Readers that already have it open keep the old copy until they close it, then the old copy is removed.
4. When a Partial file changes remotely, its present segments are dropped and refetched on demand.
5. **Free up space** on a file or folder deletes the local bodies and marks them Cloud only. It is refused for Dirty files until their upload finishes.
6. A file created or written on this device is Local by definition.

## 7. Filesystem behaviour

The mount uses `github.com/hanwen/go-fuse/v2` (the `fs` package) and must behave like an ordinary folder for every program, including editors that save atomically and file managers that move files across filesystems.

### Mount

- Mounted only after unlock; unmounted on lock or daemon stop. Mount point default `~/Vault`, configurable.
- Options: `fsname=vault`, `subtype=vault`, `default_permissions`. No `allow_other`.
- Lookups, `getattr` and `readdir` are answered from the in-memory tree and never wait on the network.
- Kernel page cache is kept for unchanged files (`FOPEN_KEEP_CACHE`) and invalidated through go-fuse's notify calls when a remote change lands.

### Operations

| Operation | Local effect | Synced as |
| --- | --- | --- |
| create, mkdir | new object ID, entry in tree and index | create |
| write, truncate | affected segments decrypted, patched, re-encrypted in the cache file | modify, after quiet period |
| rename within vault | tree and index updated | rename or move (metadata only) |
| rename over an existing file | target keeps its object ID; content replaced | modify of the target |
| unlink, rmdir | removed from tree | delete to trash |
| chmod | only the executable bit is kept | modify (metadata) |
| symlink, hard link, mknod, xattr | refused with `EPERM` or `ENOTSUP` | nothing |

### Quiet period and net effect

A change is queued for upload only after the file is closed and untouched for 2 seconds. A file created and deleted inside that window never reaches Drive or the log. This absorbs editor temp files (`.goutputstream-*`, `.swp`, `~$*`, `*~`) and atomic saves without special cases. The log records the net effect of each batch, not every syscall.

### Moving out of the vault

A move from `~/Vault` to a folder outside it reaches the daemon only as a delete; FUSE cannot see where the file went.

1. Every delete goes to the Drive trash (section 10) and is logged.
2. If the same process read the whole file in the 10 seconds before deleting it, the daemon labels it **moved out** instead of **deleted**.
3. A moved-out item raises a desktop notification: `report.pdf left the vault and is no longer tracked. Kept in trash for 30 days.`
4. Deletes inside a subtree that end with the folder's own `rmdir` within 3 seconds are coalesced into one folder-level trash entry, one Drive call and one notification (`Photos/ (412 files) left the vault`).

### Moving back in

A file arriving from outside is a new file with a new object ID; nothing is restored from trash. After the quiet period its plaintext hash is computed. If an object or trash item with the same hash exists, the body is duplicated server-side with Drive's `files.copy` instead of uploading. The same rule makes `cp` inside the vault free.

### Desktop trash

Nautilus's Move to Trash creates `.Trash-<uid>` at the mount root. The daemon accepts that folder as local-only and never syncs it. Any file landing in `.Trash-<uid>/files/` is treated as a vault delete and removed at once. The panel's trash is the real trash.

### Thumbnailers and indexers

File managers and indexers would download every media file to make previews. For Cloud-only and Partial files, reads from processes on a configurable deny list return `EIO`. The process name comes from the caller PID that FUSE supplies. Default list: `gnome-thumbnail`, `totem-video-thu`, `ffmpegthumbnailer`, `tumblerd`, `localsearch-extr`, `tracker-extract`. Local files serve them normally.

### Offline and locked

- Offline: browsing works, Local files open, Cloud-only files fail with `EIO` and the panel shows `Offline: report.pdf is not on this device`. Writes queue and upload when the network returns.
- Locked: nothing is mounted. The cache stays encrypted on disk.

## 8. Sync engine and API budget

Local changes reach Drive within a few seconds of the file going quiet, and remote changes show up within 10 to 30 seconds, while the app uses about 5 percent of Drive's per-user request limit.

### Drive limits (checked 2026-10-20)

- Drive allows 12,000 queries per 60 seconds per project and the same per user. Exceeding it returns `403 User rate limit exceeded`; backend checks can also return `429`. Google recommends truncated exponential backoff. [source](https://developers.google.com/workspace/drive/api/guides/limits)
- Push notifications (`changes.watch`) need a public HTTPS endpoint, which a laptop does not have, so the app polls `changes.list`.
- Approximate, from memory, verify before relying on it: Drive also caps uploads at about 750 GB per account per day and throttles sustained file creation above roughly 3 per second.

### Budget the app enforces on itself

| Bucket | Sustained rate | Burst | Used for |
| --- | --- | --- | --- |
| Metadata writes | 3 requests/s | 10 | create, rename, move, delete, appProperties |
| Reads and polls | 10 requests/s | 30 | changes.list, files.list, files.get |
| Content transfer | no request cap | n/a | bounded by worker counts below |

Both buckets use `golang.org/x/time/rate`. On any `403` rate-limit or `429`, the bucket's rate halves; it grows back 10 percent per error-free minute up to its ceiling. Every retry uses full-jitter backoff from 1 s up to 64 s. Retries apply to `403` rate-limit, `429` and `5xx`. A `401` refreshes the token once. A `404` on delete counts as success.

### Workers

| Pool | Default | Notes |
| --- | --- | --- |
| Uploads | 4 files in parallel | resumable sessions for files over 5 MiB, 16 MiB chunks, resume from the last confirmed byte after any failure |
| Downloads | 4 files in parallel | a single large file uses up to 4 parallel 8 MiB Range requests |
| Name decryption at unlock | `GOMAXPROCS` |  |
| Hashing | 2 | plaintext hash after the quiet period |

All pools are bounded channels with backpressure and share one `context.Context` so lock or shutdown cancels everything cleanly. Worker counts are configurable.

### Outgoing pipeline

1. FUSE handlers append an operation to `pending_ops` and return immediately.
2. The batcher waits until no new operation has arrived for 2 seconds, or 200 operations are queued, then reduces the batch to its net effect.
3. The scheduler orders the batch as a dependency graph: a folder exists before its children, a move target exists before the move. Operations on different objects run in parallel; operations on the same object run in order.
4. Bodies upload straight from the encrypted cache file. A snapshot copy is taken first so later writes cannot corrupt an upload in flight.
5. Drive metadata operations apply.
6. The batch is written as one encrypted commit to `log/<device-id>/` (section 9) and marked applied.

A crash at any step is safe: `pending_ops` replays on restart, and every Drive operation is idempotent by object ID.

### Incoming pipeline

1. Poll `changes.list` with the stored page token every 10 seconds while there was local activity or the panel was open in the last 5 minutes, otherwise every 30 seconds.
2. New commit files in other devices' `log/` folders are the source of truth. Each is downloaded, decrypted and applied in clock order.
3. Applying a commit updates the index and in-memory tree, invalidates kernel caches, and schedules background downloads for affected Local files.
4. Raw Drive changes to `tree/` that no commit explains are ignored and logged as a warning.
5. If the page token is missing or expired, the daemon rebuilds the index from a full parallel listing of `Vault/`.

### Last write wins

Every operation carries a hybrid logical clock stamp: wall time in milliseconds, a counter, and the device ID as tiebreak. An operation applies to an object only if its stamp is newer than the object's current version. If a remote write beats a local unsynced write, the local version is moved to trash with reason **superseded by \<device>** and the log records both.

## 9. Activity log graph

The log works like git: each sync batch is a commit, each device is a branch, and a commit that follows changes seen from another device is a merge. It is stored encrypted on Drive, so every device shows the same history. V1 only displays it; there is no checkout, revert or undo.

### Commit format

A commit is JSON, compressed with zstd, then encrypted with the `log` subkey.

```
{
  "id": "8c1f...",                 random 16 bytes, hex
  "device": "d-4a2e...",
  "deviceName": "amber-laptop",
  "clock": "1761055200123.0007.d-4a2e",
  "parents": ["5b90...", "e771..."],
  "ops": [
    {"kind": "create",    "oid": "...", "path": "/Notes/gate.md", "size": 2048},
    {"kind": "move",      "oid": "...", "from": "/a.pdf", "to": "/Papers/a.pdf"},
    {"kind": "moved_out", "oid": "...", "path": "/clip.mp4", "trash": "t-..."}
  ]
}
```

- `parents`: this device's previous commit, plus the newest commit of each other device that this device had applied but not yet referenced. Two parents means a merge.
- Op kinds: `create`, `modify`, `rename`, `move`, `delete`, `moved_out`, `superseded`, `trash_restore`, `trash_purge`.
- Paths are plaintext inside the commit. They are safe because the whole commit is encrypted.

### Storage

- One file per commit in `log/<device-id>/`.
- Every 500 commits a device writes a pack file holding them and deletes the singles, so a new device fetches tens of files instead of thousands. Pack and single files are fetched in parallel.
- Local copy lives in the `commits` table, so the panel renders the graph without any network call.

### What gets logged

Every change to the vault's contents or structure, from every device. Device-local actions that do not change the vault (a download, Free up space, an error) go only to the daemon's own log file, not to the graph.

## 10. Trash

Anything that leaves the vault is kept encrypted in `Vault/trash/` on Drive for 30 days, then deleted permanently.

### What lands in trash

| Reason | Trigger |
| --- | --- |
| deleted | unlink or rmdir in the mount, or Nautilus Move to Trash |
| moved out | delete that the daemon detects as a move to outside the vault |
| superseded | a local unsynced version lost to a newer remote write |

A deleted folder is one trash entry containing its whole subtree. The trash entry's metadata (original path, reason, device, time) lives in the commit that created it, so every device sees the same trash.

### Actions in the panel

- **Restore**: moves the item back to its original path. If that path is taken, it restores as `name (restored).ext`. If the parent folder no longer exists, it is recreated. Logged as `trash_restore`.
- **Delete now**: permanent delete from Drive after a confirmation dialog. Logged as `trash_purge`.
- **Empty trash**: Delete now for every item, behind a confirmation that states the item count and total size.

### Expiry

Once a day, and at each unlock, the daemon permanently deletes trash items older than 30 days. Any device may do it; a `404` from a device that lost the race counts as success. The retention period is configurable.

A file brought back into the vault after being moved out is not restored from trash. It is a new file, and its trash entry expires normally (section 7).

## 11. Device onboarding

The first device creates the vault; any later device joins it with the same Google account and password and starts with nothing downloaded.

### First device

1. Panel asks for a device name (default: hostname).
2. Google sign-in opens in the browser.
3. No `Vault/vault.header` is found, so the panel offers **Create vault**.
4. Password entered twice, at least 12 characters. The panel states plainly that a lost password cannot be recovered.
5. Daemon creates the key hierarchy, writes `vault.header`, creates the Drive folders, writes its device record, mounts `~/Vault`.

### Joining device

1. Device name and Google sign-in, as above.
2. `vault.header` is found; the panel asks for the password and unwraps the master key. A wrong password shows `Wrong password` and nothing else.
3. The panel offers two choices:
   - **Restore everything** (default, and the right choice after losing a laptop): the full tree appears at once, then every body downloads in the background until the new laptop holds a complete copy. The vault is usable during the restore; opening a file not yet restored fetches it first.
   - **On demand**: metadata only; bodies download when opened. For a second machine that only needs occasional access.
4. The daemon lists `Vault/` in parallel, fetches all commit packs and singles, builds the index, stores the changes page token, then mounts.
5. A restore shows progress in files and bytes, can be paused, resumes after a reboot, and ends with a verify pass (section 12).

The OAuth client works on any number of devices because `drive.file` access is granted per app and per Google account, not per machine.

### Every start after that

The daemon starts locked (systemd user service). The panel shows only a password field. On unlock the daemon decrypts the token and names, catches up on remote changes, then mounts.

## 12. Durability and recovery

If the laptop dies, the most that can be lost is what changed in the last few seconds, and a new laptop gets the whole vault back with the password and the Google account.

### Durability rules

1. A change is safe once its body is uploaded, verified and referenced by a commit. Until then the panel counts it as not backed up.
2. After each upload, the daemon compares Drive's `md5Checksum` with the MD5 of the ciphertext it sent. A mismatch means re-upload. A commit is written only after every body it references is verified, so the log never points at missing data.
3. Uploads of local changes always run before downloads and restores. Within a batch, small files go first, so the largest number of files becomes safe soonest.
4. `fsync` inside the mount flushes the cache file to local disk at once. It does not wait for the upload.
5. The outgoing queue lives in SQLite, so a crash or power cut resumes uploads on next unlock instead of forgetting them.

### Backup health

- Panel header shows either `All backed up · 2 min ago` or `3 files not backed up yet · 45 MB`.
- If anything stays not backed up for 10 minutes while online, a desktop notification says so.
- `vault verify` checks that every object in the index exists on Drive with a matching checksum, using one `files.list` per folder. `vault verify --deep` also downloads and decrypts every body. The shallow check runs automatically once a week and after every restore.

### First import

Moving existing data into the vault is limited by upload speed and, approximately, Drive's daily upload cap of about 750 GB per account. The panel shows the remaining bytes and an estimated time. Import the most important folders first; they are safe as soon as they finish, independent of the rest.

### Recovery kit

These must exist somewhere other than the laptop, or recovery is impossible:

- The vault password, for example in a password manager on the phone or on paper. No password, no data, by design.
- Access to the Google account, including its two-factor method.
- The project source on GitHub, or a release binary there.
- The OAuth client ID and secret, compiled into the build from a file committed to the (private) repo. Google treats desktop client secrets as non-confidential, so this is acceptable.

The first-run screen shows this list and asks the owner to confirm each item. Nothing in the kit is stored by the app.

### Recovery procedure

1. On the new laptop, build from the repo or download the release binary.
2. Start the panel, sign in to Google, enter the password.
3. Choose **Restore everything**. The tree appears at once; bodies download in the background.
4. The restore ends with `vault verify`. The panel then shows `Restore complete`.
5. In Settings, mark the dead laptop as retired. Its lane in the History graph turns grey; its commits stay.

### Recovery drill

The acceptance list (section 15) includes a full drill: wipe the app's local data on a working laptop and restore from nothing. Repeating it once a month is the only real proof the backup works.

## 13. Control panel UI

The panel is a small Wails v2 window with three views (Tree, History, Trash) and a status line; it shows only what the owner needs to act on.

Wails v2 is chosen over v3 because v3 is still a beta release while v2 remains the stable line. [source](https://github.com/wailsapp/wails/releases/tag/v3.0.0-beta.0) The frontend is plain TypeScript built with Vite, with the graph drawn in SVG. No UI framework, no component library.

### Layout

- Header: vault status (`Synced`, `Syncing 3 items`, `Offline`, `Locked`), transfer speed while active, a **Lock** button, a settings icon.
- Left rail: Tree, History, Trash.
- Locked state: the whole window is one password field and an Unlock button.

### Tree view

- Collapsible folder tree with name, size and a state icon (Cloud only, Partial, Local, Dirty).
- Right-click: Open, Show in Files, Download, Free up space.
- Folder rows show aggregate size and how much of it is on this device.

### History view

- Left: the commit graph. One vertical lane per device, newest at top, a dot per commit, lines to parents. A line crossing lanes is a merge. Each device has a fixed colour.
- Right of each dot: time, device name, one-line summary (`3 created, 1 moved, 1 moved out`).
- Click a commit to expand its operations with paths.
- A path filter at the top shows only commits that touched that path or folder.
- Loads 200 commits at a time on scroll.

### Trash view

Table with name, original path, reason, device, deleted on, expires in. Row actions Restore and Delete now. An Empty trash button with confirmation.

### Settings

Mount point, device name, worker counts, polling intervals, trash retention days, thumbnailer deny list, Sign out of Google.

### Outside the panel

- Desktop notifications come from the daemon through `org.freedesktop.Notifications` on D-Bus, so they appear even when the panel is closed.
- A small CLI, `vault`, talks to the same daemon API: `vault status`, `vault lock`, `vault download <path>`, `vault free <path>`.
- Two Nautilus scripts installed in `~/.local/share/nautilus/scripts/` call the CLI, giving right-click **Scripts → Download** and **Scripts → Free up space** in the file manager.

## 14. Project structure and code rules

One Go module, three binaries, and one package per concern so a reader can find any behaviour from the folder names alone.

```
vault/
  cmd/
    vaultd/            daemon: wiring, flags, signal handling
    vault/             CLI client
  panel/               Wails v2 app
    frontend/          TypeScript + Vite, SVG graph
  internal/
    config/            load and validate config.toml
    secret/            mlocked key buffers, zeroing
    crypto/            KDF, key wrapping, subkeys, name and segment encryption
    vaultfile/         VLT1 header and segment reader/writer with random access
    drive/             Drive client, rate limiter, resumable upload, ranged download, changes
    auth/              OAuth flow, encrypted token store
    index/             SQLite store and migrations
    tree/              in-memory tree with decrypted names
    cache/             sparse cache files and segment bitmaps
    fusefs/            go-fuse nodes and handles
    syncer/            batcher, scheduler, upload and download pools, remote poller
    journal/           commits, hybrid logical clock, packs
    trash/             trash entries, restore, purge, expiry
    procfilter/        thumbnailer and indexer deny list
    notify/            D-Bus desktop notifications
    api/               Unix socket HTTP server and client, event stream
    daemon/            lock and unlock lifecycle, ties the packages together
  packaging/
    systemd/vaultd.service
    nautilus/          Download and Free up space scripts
```

### Dependencies

| Need | Package |
| --- | --- |
| FUSE | `github.com/hanwen/go-fuse/v2` |
| Crypto | `golang.org/x/crypto` (argon2, chacha20poly1305, hkdf), stdlib `crypto/sha256` |
| Drive and OAuth | `google.golang.org/api/drive/v3`, `golang.org/x/oauth2` |
| SQLite | `modernc.org/sqlite` |
| Concurrency | `golang.org/x/sync/errgroup`, `golang.org/x/time/rate` |
| Compression | `github.com/klauspost/compress/zstd` |
| D-Bus | `github.com/godbus/dbus/v5` |
| Panel | `github.com/wailsapp/wails/v2` |

Nothing else without a stated reason in the pull request.

### Daemon API

The daemon listens on `$XDG_RUNTIME_DIR/vault/daemon.sock`, mode 0600, and checks the peer's UID with `SO_PEERCRED` on every connection. It speaks JSON over HTTP: `POST /unlock`, `POST /lock`, `GET /status`, `GET /tree?path=`, `GET /commits?before=&limit=&path=`, `GET /trash`, `POST /trash/restore`, `POST /trash/purge`, `POST /download`, `POST /free`, `GET /events` (server-sent events for live status). The panel and CLI share the client in `internal/api`.

### Running a second instance on one machine

The owner has one laptop, so every multi-device behaviour must be testable on it. All three binaries accept `--home <dir>` (default `~/.local/share/vault`). Everything an instance owns lives under that directory: config, index, cache, token, socket, device ID, daemon log. Its mount point is set in its own config.

```
vaultd --home ~/vault-b          # second daemon, mounts ~/VaultB, its own device ID
vault  --home ~/vault-b status   # CLI talking to instance B
vault-panel --home ~/vault-b     # panel for instance B
```

To Drive and to the log, instance B is simply another device. Deleting its home directory is exactly the state of a brand-new laptop.

### Code rules

1. Names explain the code. Functions are verbs (`encryptSegment`, `scheduleUpload`, `applyRemoteCommit`), types are nouns. No abbreviations beyond `ctx`, `err`, `id`.
2. No comments that restate the code. Allowed: a package doc line, and a short why-comment where the reason is not visible (associated-data layout, a Drive quirk).
3. Files under about 300 lines, functions under about 50. One responsibility each.
4. No global mutable state. Dependencies arrive through constructors. Interfaces are declared by the package that consumes them, so `syncer` depends on a small `RemoteStore` interface, not on `drive` directly.
5. Every goroutine has an owner and a `context.Context`; fan-out uses `errgroup` with `SetLimit`. No unbounded channels.
6. Errors are wrapped with `%w` and context. No panics outside `main`.
7. Logging through `log/slog`, never with file names or contents.
8. `go vet`, `staticcheck` and `go test -race ./...` pass on every commit.

### Tests

- `crypto` and `vaultfile`: round trips, random-access reads and writes at segment edges, tampering, reordering and truncation all fail.
- `drive`: an in-memory fake implementing the same interface, with injectable `403`, `429` and `5xx` responses.
- `syncer` and `journal`: two simulated devices on one fake Drive; ordering, last write wins, superseded-to-trash, crash and replay.
- `fusefs`: integration tests on a real temporary mount (needs `/dev/fuse`): atomic save in an editor pattern, cross-filesystem move out, folder move out coalescing, `cp` dedup.

## 15. Acceptance criteria and build order

V1 is done when every box below can be ticked on one Ubuntu laptop. Laptop B in these checks means a second instance started with --home (section 14).

### Acceptance

- [ ] Create a vault on laptop A; `~/Vault` appears after unlock and disappears on lock.
- [ ] Drive's web UI shows only `Vault/` with encrypted names under `tree/`; no file body opens as anything readable.
- [ ] No file under `~/.local/share/vault/` contains a plaintext file name or content (checked with `grep` for known strings).
- [ ] After a reboot, nothing mounts until the password is entered; `strings` on a core-dump attempt and on swap shows no key.
- [ ] Copy a 4 GB video into the vault; it uploads with resumable sessions; killing the daemon mid-upload and restarting resumes, not restarts.
- [ ] Laptop B joins with On demand; the full tree appears in under 30 s for 10,000 files and nothing downloads.
- [ ] On B, the 4 GB video plays in VLC within 3 s and seeking works without a full download.
- [ ] Nautilus browsing a folder of 500 Cloud-only photos does not trigger downloads.
- [ ] Edit a document in LibreOffice on A; B shows the new version within 30 s; the History view shows the commit under A's lane.
- [ ] Rename and move a folder of 1,000 files on A; Drive receives metadata calls only, no uploads.
- [ ] Drag a file from `~/Vault` to `~/Downloads`: notification shown, item in Trash with reason moved out, logged.
- [ ] Drag a folder of 400 files out: one notification, one trash entry.
- [ ] Drag the same file back: logged as created, no upload (server-side copy), trash entry untouched.
- [ ] Restore and Delete now work from the Trash view; items older than 30 days vanish automatically.
- [ ] Unsynced edit on A loses to a newer edit on B: A's version is in trash as superseded.
- [ ] Free up space on a 10 GB folder returns the disk space and the files stay browsable.
- [ ] A sync of 5,000 small files never produces more than three `403` or `429` responses, and finishes.
- [ ] Pulling the network mid-sync and restoring it completes the sync with no duplicates or lost files.

### Recovery drill

All on one laptop. Instance A is the normal install; instance B is a second instance started with `--home ~/vault-b`.

- [ ] With 50 GB in the vault, save a file in `~/Vault`, then within 5 seconds `kill -9` daemon A (simulates the laptop dying). Start a fresh instance B and choose Restore everything: every file is present except at most that last save, and `vault --home ~/vault-b verify --deep` passes.
- [ ] Delete instance B's home directory and restore again from only the password and Google sign-in: a complete vault comes back.
- [ ] `kill -9` daemon B in the middle of a restore; on restart it resumes without re-downloading finished files.
- [ ] Edit a file in `~/Vault`; within 30 seconds instance B's mount shows the change, and both panels show the commit in A's lane.
- [ ] Corrupt one uploaded body on Drive by hand; `vault verify --deep` reports exactly that file.
- [ ] The panel header never says `All backed up` while any change is still unverified.

Optional, for a real power cut: run instance B inside a VM, and pull the plug on the VM instead of using `kill -9`.

### Build order

Each step ends with passing tests before the next starts.

1. `crypto`, `secret`, `vaultfile`: key hierarchy, names, segment format.
2. `drive` with the fake, rate limiter, resumable upload, ranged download. `auth` with the encrypted token store.
3. `index`, `tree`, `cache`.
4. `fusefs` over a purely local store (no sync): full POSIX behaviour, quiet period, atomic saves.
5. `syncer` outgoing pipeline, then `journal` commits.
6. `syncer` incoming pipeline, last write wins, second-device join, Restore everything, upload verification and vault verify.
7. `trash`, move-out detection, coalescing, server-side copy dedup, `procfilter`.
8. `api`, CLI, `notify`, systemd unit.
9. Panel: locked screen, Tree, History graph, Trash, Settings. Nautilus scripts.
10. Run the acceptance list on two machines.
