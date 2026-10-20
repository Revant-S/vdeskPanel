# Arbitration: Vault v1 plan (01) vs review (02)

The arbiter read the spec, plan and review in full and checked the main claims against source.

- **R9 confirmed** in go-fuse `fs/bridge.go`: `rawBridge.Rename` calls the node's `Rename`, then `p1.MvChild(oldName, p2, newName, true)`. The source Inode moves to the new name and the target is unlinked; the kernel dcache does the same.
- **R18:** the kernel fills `in.h.pid` from `task_pid(current)`: the thread ID, not the TGID.
- **Not checked:** memguard behaviour (zero-size buffer on error, no rekey goroutine for LockedBuffer) relies on the reviewer's check.

## 1. Summary

| # | Ruling | Gist |
|---|---|---|
| R1 | ACCEPT-MODIFIED | Full body fetched on first mutation of a non-Local object, not at open. `Snapshot` refuses an incomplete body. |
| R2 | ACCEPT-MODIFIED | Snapshot is a re-encrypting copy under a fresh file key and BodyID; swapped into the cache after commit. |
| R3 | ACCEPT-MODIFIED | Every segment verified while snapshotting. Bad present segment refetched only for clean entries; Dirty → EIO + Corrupt. |
| R4 | ACCEPT | BodyID in index and commit ops, checked on fetch; file-key AD fixed. |
| R5 | ACCEPT | Name AD includes raw oid. NAME_MAX 255. |
| R6 | ACCEPT | Keyring with one prebuilt AEAD per subkey; one AEAD per open Body. |
| R7 | ACCEPT-MODIFIED | No `mlockall` by default; check memguard `Size()==0`; wipe KEK and password; bound KDF params. |
| R8 | ACCEPT-MODIFIED | Final Lock order below, with a bounded upload drain (A1). |
| R9 | ACCEPT-MODIFIED | fusefs nodes hold only an ino; workspace owns a mutable ino↔oid map; handles on a replaced target are detached. |
| R10 | ACCEPT-MODIFIED (implement) | Path-reuse adoption in the reducer, narrowly scoped, using the R9 map. |
| R11 | ACCEPT-MODIFIED | Global delete hold (3 s quiet, 30 s cap); cap applies to net ops after reduction; batches strictly sequential. |
| R12 | ACCEPT-MODIFIED | `files.generateIds` for every app-created object; replay does Get first; 409 = exists. |
| R13 | ACCEPT | Snapshots at a fixed path, fsynced, MD5 recorded, kept until commit. |
| R14 | ACCEPT | Start token before listing; token saved in the same transaction as the page's commits. |
| R15 | ACCEPT-MODIFIED | Log is authoritative; orphans ignored, logged, GC'd by any device after 30 days. Winner-commit trash target rejected. |
| R16 | ACCEPT-MODIFIED | Two LWW registers plus a delete tombstone; final decision table below. |
| R17 | ACCEPT-MODIFIED | Remote content waits while a writable handle is open; stamp at enqueue; remote modify switches to a new Partial body. |
| R18 | ACCEPT-MODIFIED | TID→TGID; "whole read" also counts a read-only open granted keep-cache. |
| R19 | ACCEPT | `modify + delete` of a synced object uploads the latest body into trash. |
| R20 | ACCEPT | Password bodies are raw octet-stream, capped, wiped. |
| R21 | ACCEPT-MODIFIED | Socket dir 0700 (owner+mode checked), bind, chmod 0600; no process umask; stale socket removed only after failed connect. |
| R22 | ACCEPT | `--drive-endpoint` only under `devdrive` build tag. |
| R23 | ACCEPT-MODIFIED | Drive header fetched after unlock: before mount if online, in background if offline. |
| R24 | ACCEPT-MODIFIED | Cross-boundary data types in `internal/model`; helper renamed `plaintextcheck`. |
| R25 | ACCEPT-MODIFIED | Drop `pkg/browser` and drivefake persistence; prefer generated Wails bindings. |
| R26 | ACCEPT | `trash_purge` logged only on 2xx delete. |
| R27 | ACCEPT | Local-only files are VLT1 with encrypted names; delete matching `.trashinfo`. |
| R28 | ACCEPT | `Copy` 404 falls back to upload. |
| R29 | ACCEPT | 404 on a listed single re-lists that device's folder. |
| R30 | ACCEPT-MODIFIED | Free refused for Dirty, Corrupt or open-writer objects; readers fine. |
| R31 | ACCEPT | Unlock catch-up time-bounded; mount, keep polling. |
| A1–A9 | see below | Problems both missed. |

## 2. Detailed rulings

**R1. ACCEPT-MODIFIED.** Spec §6.6 says a written file is Local; a sparse upload corrupts the mirror. Fetch on first `WriteAt` or non-zero `Truncate` of a CloudOnly/Partial object, not at open-for-write (many apps open O_RDWR and only read). Fetch runs outside workspace locks and blocks only that write. Truncate to 0 needs no fetch. Offline → EIO. `cache.Snapshot` returns `ErrIncompleteBody` unless the bitmap is full. Test: write 1 byte mid Cloud-only file, sync, deep-verify on B.

**R2. ACCEPT-MODIFIED.** Spec §4.5 requires a per-version key; under D7 every version is a full upload and ext4 snapshots are full copies, so the extra cost is CPU only.
- The snapshot pass decrypts each live-cache segment and re-seals under a fresh file key and BodyID, computing plaintext SHA-256 (content MAC) and ciphertext MD5 in the same pass. This folds the hashing pool into the snapshot pass ("Hashing 2").
- After commit, if no newer op and no writable handle, `cache.Replace(oid, snapshotPath)` makes the uploaded body the cache body; otherwise the snapshot is deleted.
- Cache invariant: an entry is either (a) complete, any BodyID, or (b) a sparse copy of exactly the Drive body whose BodyID equals `objects.body_id`. Range fetches only in (b).
- D8 and D20 void; FICLONE dropped.
- The "replay old segment" test now must fail.

**R3. ACCEPT-MODIFIED.** If snapshot verification fails nothing uploads and the old Drive version stays; object gets `local_state=Corrupt`, shown in panel, notification fires. On read, a bitmap-present segment failing AEAD is cleared and refetched only for clean entries (case b); Dirty or local-authored complete entries return EIO and flag Corrupt.

**R4. ACCEPT.** `objects.body_id` and `journal.Op.BodyID`. On fetch, header BodyID must equal `objects.body_id`; on mismatch poll changes once and retry, else `vaultfile.ErrTampered`. After dedup `Copy`, `objects.body_id` is the source's BodyID; the local complete body with a different BodyID is allowed under the R2 invariant.

**R5. ACCEPT.** Raw 16-byte oid in name AD. Root has no name. Names over 255 bytes → `ENAMETOOLONG`.

**R6. ACCEPT.** `crypto.Keyring` builds one `cipher.AEAD` per subkey at unlock. `Keyring.Use(func(*Keys) error)` holds an RLock; `Destroy` takes the write lock; `Use` after destroy → `ErrLocked`. `vaultfile.Body` builds its AEAD once at open. `Seal(aead cipher.AEAD, …)`. Residual risk documented: AEAD's internal key copy on the Go heap.

**R7. ACCEPT-MODIFIED.** `HardenProcess` always sets `PR_SET_DUMPABLE=0`, `RLIMIT_CORE=0`; `mlockall(MCL_CURRENT|MCL_FUTURE)` only if `RLIMIT_MEMLOCK` is infinite or `lock_all_memory=true`. `/status` reports `keysMaySwap` when off. `secret.NewKey` errors on `Size()==0`. Wipe password, Argon2 output, KEK after unwrap. `ParseVaultHeader` bounds: Time 1–10, MemoryKiB 64 MiB–4 GiB, Threads 1–16, salt 32 bytes, wrapped VMK 72 bytes.

**R8. ACCEPT-MODIFIED.** Final Lock order (also SIGTERM / daemon stop):
1. State `Locking`; API refuses mutations.
2. `fusefs.Unmount()`. On EBUSY: `workspace.Freeze()` (all Store ops EIO, handles detached, Release enqueues nothing; Dirty rows replay on next unlock), then `fusermount3 -u -z`.
3. `Outgoing.Drain(ctx, LockFlushTimeout=20s)`, ignoring quiet periods; skipped offline.
4. Cancel root ctx; wait for every owned goroutine.
5. Close index and cache.
6. `Keyring.Destroy()`; wipe in-memory OAuth token.
7. State `Locked`.

systemd `TimeoutStopSec=60`. Test: Lock with a file held open externally and a read in flight.

**R9. ACCEPT-MODIFIED.** fusefs nodes hold only an ino; the workspace owns the ino↔oid map (`InodeOf`, `ObjectOf`). Rename-over rebinds the source ino to the target oid; the old target ino becomes `detached`. Handles on the target opened before the replace are detached (read old Entry, writes not recorded). Invalidations resolve oid→ino through the store. Test (gedit): after save, stat/reopen/write/second save hit the target oid; a reader holding the old file sees old bytes.

**R10. ACCEPT-MODIFIED, implement.** If a batch contains a net delete of synced file X (not moved_out), a net create of never-synced file Y, and Y's final path equals X's pre-batch path → `modify X` with Y's body. The workspace rebinds Y→X in tree, index, ino map and cache file in one transaction. File↔file only.

**R11. ACCEPT-MODIFIED.**
- Non-delete ops ready per plan (close + 2 s quiet). A batch forms after 2 s with no new op, or at 200 ready ops.
- Deletes (unlink, rmdir, moved_out) ready only when no delete has arrived for `DeleteHold=3s`, capped at `MaxDeleteHold=30s`; all held deletes released together.
- Reduce over the entire ready set; take at most 200 net ops in dependency order, small files first; rest stays queued.
- Batches run strictly one at a time; levels sequential, ops within a level parallel.
- Test: MaxBatch=10, 25 files, `rm -r` → one folder-level delete.

**R12. ACCEPT-MODIFIED.** `files.generateIds` (reads bucket) for every app-created object (tree objects, versions, trash uploads, commits, packs, device records, Copy targets), stored in the persisted batch before any create. Replay: `Get(id)`; exists with matching `oid` → done; 404 → create with `id=`; 409 → exists, Get. Fake implements all of it. `FindByProperties` only for rebuild. Drop `attempts > 0` and `Record`'s name check.

**R13. ACCEPT.** `cache/snapshots/<batchID>/<oid>`, fsynced, path and MD5 in the batch before session start, removed or swapped only after commit. On resume, missing snapshot or MD5 mismatch → discard session, re-snapshot, restart.

**R14. ACCEPT.** `fields` mask includes `parents`, `appProperties`, `md5Checksum`, `size`, `createdTime`, `trashed`. Test: commit lands on A during B's join listing; B sees it.

**R15. ACCEPT-MODIFIED.** Orphan = Drive file under `tree/` or `trash/` with our `vid` whose Drive ID is not referenced by any object/version/trash row in the applied index, nor by this device's open batches. Ignored and logged. Daily expiry pass: any device deletes orphans with `createdTime` > 30 days; 404 = success. Winner-commit trash target REJECTED (rare case, bounded by GC, no data loss).

**R16. ACCEPT-MODIFIED.** Three stamps per object: `content_ver` (body_id, drive_id, size, mtime, executable, content_mac; set by create/modify/chmod/utimens), `placement_ver` (parent, enc_name; create/rename/move), `delete_ver` (tombstone on trashed rows). A remote op applies to a register only if newer. `HLC.Observe` never moves the local clock past now + 1 h; such stamps logged.

| # | Concurrent ops | Outcome | Fix-up |
|---|---|---|---|
| 1 | modify vs modify | Newer `content_ver` wins | Loser's author trashes its body, `superseded by <winner>`, logs `superseded`. Unsynced → upload into `trash/`; uploaded → move Drive file into `trash/`. |
| 2 | move vs move | Newer `placement_ver` wins | None |
| 3 | modify vs move | Both apply | None |
| 4 | delete vs modify, delete newer | Delete wins | Modify's author trashes its body as row 1 |
| 5 | delete vs modify, modify newer | Object stays live with new content; delete's trash entry keeps the older version | `live = content_ver > delete_ver`. Restore while live → new oid, `name (restored).ext` |
| 6 | file delete vs move | Delete wins regardless of stamps (deliberate) | None; restorable from trash |
| 7 | folder delete vs op on a descendant | Folder delete wins; descendant inside the folder's trash entry | Descendant author logs `superseded` (`parent deleted on <device>`) and notifies |
| 8 | name collision | Older `placement_ver` renamed `name (conflict <author device>).ext`, then ` 2`, ` 3`… | Every detecting device logs the rename; converges |
| 9 | move cycle | Newer move wins | Older move's author logs a move back to `From` (or root) |
| 10 | restore vs restore | Newer placement wins | None |
| 11 | purge vs restore | Purge wins | Restorer gets 404, drops, warns |
| 12 | create vs anything | Only row 8 applies | — |

One two-device test per row.

**R17. ACCEPT-MODIFIED.**
- (a) Remote content for an object with an open writable handle goes to `deferred_content`. At Release: if the handle wrote, the local modify (stamped at enqueue, after Observe) wins and the remote author supersedes per row 1; if not, deferred content is applied.
- (c) Every op's stamp is taken when appended to `pending_ops`.
- (b) Remote modify of an unopened file switches atomically to the new drive_id, body_id, size, and a new sparse cache file renamed over `cache/<oid>`; state Partial; background download refills if it was Local.
- keepCache only if `content_ver` unchanged since the kernel last cached this ino.
- Remote move invalidates both parents.

**R18. ACCEPT-MODIFIED.** TID→TGID via `/proc/<tid>/status`; detector and procfilter key on TGID; `/proc/<tgid>/comm`. "Read whole recently" = TGID's reads covered the file, or TGID opened it read-only with keep-cache on an unchanged Local file, within 10 s. False positive only changes the label. Test with a multi-threaded helper.

**R19. ACCEPT.** `modify + delete` (or moved_out) of a synced object snapshots the latest body and uploads it into `trash/`; old version deleted after commit. `create + delete` still dropped.

**R20. ACCEPT.** `/unlock`, `/create`, `/join`: raw octet-stream password ≤1 KiB, pre-allocated buffer, wiped after Argon2. Other params in query. Document net/http buffers and JS strings as unwipeable.

**R21. ACCEPT-MODIFIED.** Directory 0700 with owner+mode check (refuse otherwise), bind, chmod 0600; stale socket removed only after failed connect; SO_PEERCRED kept. No process umask (racy).

**R22. ACCEPT.** **R23. ACCEPT-MODIFIED:** cached header derives keys; once the token decrypts, if online fetch Drive `vault.header` before mount and refuse on vault-ID mismatch, newer format or different wrapped VMK; if offline, check on first connectivity (newer format stops sync, doesn't unmount).

**R24. ACCEPT-MODIFIED.** `model.RemoteFile`, `RemoteChange`, `ChangesPage`, `Entry` in `internal/model`. Helper renamed `internal/plaintextcheck`.

**R25. ACCEPT-MODIFIED.** Drop `pkg/browser` (CLI prints URL + `xdg-open`; panel uses `runtime.BrowserOpenURL`). Drop drivefake persistence. Generate Wails bindings via `go run …wails@v2.17.0 generate module`; hand-written bridge only as fallback. State memguard's transitive deps.

**R26–R29, R31. ACCEPT** as written. **R30. ACCEPT-MODIFIED:** `Free` → `ErrBusy` for Dirty, Corrupt or open-writer; readers fine. Unlock catch-up bounded at `UnlockCatchUpTimeout=15s`.

### Additional problems both missed

- **A1.** Lock/shutdown strand quiet-period changes → bounded `Outgoing.Drain` in Lock step 3; `TimeoutStopSec=60`.
- **A2.** A file held open for writing never uploads → Dirty file with open writer enqueues `modify` after fsync + 2 s quiet, or 60 s without writes (`OpenDirtyMaxAge`).
- **A3.** `synchronous=NORMAL` can lose the Dirty mark on power loss → WAL with `synchronous=FULL`. Replaces D40.
- **A4.** Partial reader gets 404 after D7 deletes the old version → poll once, apply, retry; else EIO.
- **A5.** fusefs↔workspace back-reference resolved by the R9 ino map.
- **A6.** One `vault/v1/<purpose>` AD scheme (below).
- **A7.** Restore while oid live → new oid, `(restored)` name.
- **A8.** FICLONE removed.
- **A9.** A `Copy` is verified only when its `md5Checksum` equals the source's `body_md5`.

## 3. Final binding amendments

Where plan and this list conflict, this list wins.

### Format reference (Step 1)

All AEAD is XChaCha20-Poly1305: nonce(24) ‖ ciphertext ‖ tag(16). Big-endian integers; ASCII labels.

| Item | Key | Associated data |
|---|---|---|
| VMK wrap | Argon2id KEK | `"vault/v1/header"` ‖ vaultID(16) |
| Name | name subkey | `"vault/v1/name"` ‖ vaultID(16) ‖ oid(16 raw) |
| Wrapped file key | content subkey | `"vault/v1/file-key"` ‖ u16(formatVersion) ‖ BodyID(16) |
| Segment | per-body file key | BodyID(16) ‖ u64(index) ‖ u8(final) |
| Commit | log subkey | `"vault/v1/commit"` ‖ commitID(16) |
| Pack | log subkey | `"vault/v1/pack"` ‖ packFileName |
| Device record | log subkey | `"vault/v1/device"` ‖ deviceID |
| Token | token subkey | `"vault/v1/token"` ‖ vaultID(16) |
| Index sealed column | index/seal | `"vault/v1/index/<table>.<column>"` ‖ row primary key |

HKDF salt = vaultID; info = `vault/v1/<purpose>`; index expanded into `index/seal` and `index/mac`. VLT1 header (128 bytes): `VLT1` ‖ u16 version=1 ‖ BodyID(16) ‖ wrapped file key(72) ‖ zero padding.

### Step 0
1. Test helper `internal/plaintextcheck`.
2. `model.Entry`, `RemoteFile`, `RemoteChange`, `ChangesPage`, `Ino`.
3. `LocalState` `Corrupt`.

### Step 1
1. `HardenProcess` per R7; `NewKey` errors on `Size()==0`.
2. `crypto.Keyring` (prebuilt AEADs, RWMutex, `Use`/`Destroy`, `ErrLocked`); `Seal`/`Unseal` take `cipher.AEAD`.
3. Wipe password, Argon2 output, KEK; KDF bounds in `ParseVaultHeader`.
4. AD table above; names >255 bytes → `ENAMETOOLONG`.
5. Body identity immutable; new key and BodyID only via a new Body.
6. `vaultfile.Reseal(src, dst, contentAEAD, random) (Header, sha256, md5, error)`, verifying every segment, `ErrTampered` naming the segment.
7. Tests: replay-old-version fails; Reseal round-trip; Reseal on corrupted segment.

### Step 2
1. drive returns model types.
2. `GenerateIDs(ctx, n)`; create/resumable/copy accept a pre-generated ID; 409 → `ErrAlreadyExists`.
3. `fields` mask per R14.
4. Fake: generateIds, create/copy with `id`, 409, `createdTime`; no persistence.
5. `tools/drivefake` and `--drive-endpoint` under `devdrive`.
6. No `pkg/browser`.

### Step 3
1. WAL + `synchronous=FULL`.
2. `objects`: `body_id`, `content_ver`, `placement_ver`, `delete_ver`, `deferred_content`.
3. `outgoing_batches`: `pregenerated_ids`, per net op snapshot path/MD5, session URI, offset.
4. Cache invariant (R2); header BodyID checked on fetch.
5. Read-path AEAD failure handling (R3).
6. `Snapshot(batchID, oid)` via Reseal into `cache/snapshots/<batch>/<oid>`, fsynced; `ErrIncompleteBody`; no FICLONE.
7. `Free` → `ErrBusy` for Dirty/Corrupt/open-writer.
8. Local-only entries VLT1 with encrypted names.

### Step 4
1. Workspace owns ino↔oid map; fusefs nodes store only ino.
2. Rename-over rebinds source ino to target oid; old target ino detached; its handles detached.
3. First mutation of CloudOnly/Partial fetches full body outside locks (except truncate 0); offline EIO.
4. Stamps at enqueue; open Dirty writers enqueue modify after fsync+2 s or 60 s idle.
5. `Freeze()`.
6. `.Trash-<uid>`: rename into `files/X` deletes and removes `info/X.trashinfo`.
7. Reducer: R10 adoption; R19 modify+delete; editor tests assert one modify.
8. Tests: gedit (R9), vim one modify, old reader keeps old bytes, Cloud-only write triggers full fetch.

### Step 5
1. Batching per R11 (test MaxBatch=10, 25 files).
2. Pipeline: collect+reduce → generate IDs → persist batch → snapshot via Reseal (Corrupt on ErrTampered, keep old version) → dedup Copy (pre-ID, 404 fallback, MD5 check) → execute by levels → verify MD5 → record commit → delete old versions → swap snapshot into cache or delete → clean pending ops.
3. Replay uses `Get(preID)`; 409 = success.
4. Resume checks snapshot + MD5; else re-snapshot and restart.
5. `journal.Op.BodyID`; `Record` uses a pre-generated ID.
6. HLC `Observe` ≤ now + 1 h.
7. `Outgoing.Drain(ctx, timeout)`.

### Step 6
1. Page-token rules (R14) + mid-join test.
2. Apply per R16 table, one two-device test per row.
3. `deferred_content` for open writers.
4. Remote modify switches to a new sparse body; keepCache rule; both parents invalidated.
5. Range-fetch 404/BodyID mismatch → poll, apply, retry, else EIO.
6. Orphans per R15.
7. Pack single 404 → re-list and fetch pack.
8. Unlock catch-up ≤15 s.

### Step 7
1. TID→TGID; whole-read rule; multi-threaded test helper.
2. `trash_purge` only on 2xx.
3. Restore while live → new oid.
4. Copy 404 → upload.

### Step 8
1. Lock order (R8), `LockFlushTimeout=20s`; test held-open file and in-flight read.
2. Unlock with Drive header check (R23).
3. Octet-stream passwords (R20).
4. Socket rules (R21).
5. systemd `TimeoutStopSec=60`.
6. Config keys: `lock_all_memory` (false), `open_dirty_max_age` (60 s), `delete_hold` (3 s), `max_delete_hold` (30 s), `lock_flush_timeout` (20 s), `unlock_catchup_timeout` (15 s).
7. Status shows `Corrupt` and `keysMaySwap`.

### Step 9
1. Generated Wails bindings; hand-written bridge only if generation fails.
2. Show Corrupt and `superseded` / `parent deleted` entries.

### Superseded plan decisions
D8, D14, D16 (now with the R9 ino map), D20, D24, D29, D33, D39, D40 replaced as above. D19 kept for R16 rows 1 and 4.
