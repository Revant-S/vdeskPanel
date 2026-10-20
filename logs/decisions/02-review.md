# Review of `logs/decisions/01-plan.md` (Reviewer)

## Version and dependency claims I checked

Checked against proxy.golang.org; all accurate: go-fuse v2.12.0 (2026-10-09), x/crypto v0.58.0 (declares `go 1.26.0`), memguard v0.23.0, backoff/v5 v5.0.3, esiqveland/notify v0.14.0, go-sse v0.11.0, bitset v1.26.0, adrg/xdg v0.5.3, wails/v2 v2.17.0 (2026-10-18), sqlite v1.60.1, klauspost/compress v1.20.3, staticcheck v0.8.2 (= 2026.2.2). Toolchains go1.26.5/.7/.8 are in the module cache; installed `go` is 1.22.2, so `GOTOOLCHAIN=auto` is required.

Library behaviour checked: memguard's `NewBuffer` returns a **zero-size buffer on error instead of an error**. memguard's rekey goroutine only starts if Enclave is used, not for LockedBuffer. Drive `files.generateIds` lists `drive.file` as an allowed scope.

## Problems

**R1. Blocker. Step 3 cache / Step 5 outgoing. A Partial or Cloud-only file that gets written can upload with holes.**
`cache.Entry.WriteAt` "fetches edge segments if absent", leaving the object Partial and Dirty. Step 5 uploads "bodies from the snapshot" of a sparse file, pushing a corrupted body that the commit references. Spec §6.6: "A file created or written on this device is Local by definition."
Fix: on first write / open-for-write of a non-Local object (except `O_TRUNC` to 0), fetch the full body first, outside workspace locks. `Snapshot` refuses an incomplete body. Test: open Cloud-only file, write 1 byte mid-file, sync, deep-verify on device B.

**R2. Major. Step 1 vaultfile, D8. Contradicts spec on per-version file keys; leaves rollback hole.**
Spec §4.5: "Each file version gets its own random 32-byte file key". The plan keeps key and BodyID across in-place edits to avoid re-encrypting GBs, but under D7 every modify re-uploads the whole body anyway, and on ext4 the snapshot is a full copy. Keeping the key leaves the segment-rollback attack open.
Fix: snapshot is a re-encrypting copy: decrypt each segment from the live cache, re-seal under a fresh file key and BodyID. Same I/O as the planned copy, plus a free integrity check (R3). Record the uploaded version's BodyID in index and commit op (R4); never range-fill a cache entry from a Drive body that differs from it.

**R3. Major. Step 5 durability. Torn or corrupt local segments get uploaded unchecked.**
A power cut mid-rewrite can leave a segment that fails AEAD; on restart D15 re-enqueues `modify` and ships it. MD5 only proves transport. Drive's previous good version is then deleted (D7).
Fix: decrypt-verify every segment while building the snapshot (R2 does it free). On failure don't upload; keep the old Drive version, flag the object, surface in panel. In the cache read path, a bitmap-present segment that fails AEAD is treated as absent and refetched.

**R4. Major. Crypto, Step 1 and D17. Nothing binds a body to its object; Google can swap whole bodies.**
Segment AD is BodyID‖index‖final; BodyID is not the oid; nothing ties a downloaded body to its object. Spec §4 says AD "stops ... swapping between files".
Fix: add `BodyID` to `journal.Op` and `objects` (commit is authenticated by the log key). Check header BodyID on download/open. Specify wrapped-file-key AD: `"file-key/v1"‖BodyID‖formatVersion`. After dedup `Copy`, the object's BodyID becomes the source's; local cache body must be replaced or recorded as differing so range fetches never mix bodies.

**R5. Major. Crypto, D14. Name AD should include the oid.**
With AD = vaultID only, Google can swap two objects' encrypted names undetected. The oid never changes across rename/move, so including it doesn't force re-encryption on move.
Fix: AD = `"name/v1"‖vaultID‖oid`. Enforce NAME_MAX 255 bytes.

**R6. Major. Crypto API `Seal(key *secret.Key, ...)`. AEAD keys copied onto the heap per call.**
`chacha20poly1305.NewX(key.Bytes())` per op leaves an unwiped `[32]byte` heap copy per segment.
Fix: one AEAD per subkey built at unlock, held in a `Keyring`, dropped on lock; one AEAD per open Body. Document residual heap copy of long-lived keys.

**R7. Major. `secret`, D29. Best-effort `mlockall(MCL_FUTURE)` can crash the daemon; memguard fails silently.**
With `MCL_FUTURE`, all heap/stack/mmap growth counts against `RLIMIT_MEMLOCK` (Argon2 256 MiB, upload chunks, FUSE buffers); hitting it makes the Go runtime fatal. A user unit can't raise `LimitMEMLOCK` past the user manager's hard limit. memguard returns zero-size buffers on mlock failure.
Fix: `mlockall` only if `RLIMIT_MEMLOCK` is infinite or behind opt-in config; else rely on memguard and report `keysMaySwap`. Check `Size()==0` in `NewKey` (+ test). Wipe KEK and password after unwrap. Bound KDF params when parsing `vault.header`.

**R8. Major. Step 8 Lock. Destroying keys with requests in flight can segfault.**
memguard unmaps destroyed buffers; in-flight views SIGSEGV. `Unmount` returns EBUSY with open files.
Fix: Lock order: (1) store returns EIO once locking starts; (2) lazy unmount on EBUSY; (3) cancel root ctx; (4) wait for errgroups and serve loop; (5) destroy keyring. RWMutex-guarded `Keyring`. Test Lock with a file held open and a read in flight.

**R9. Major. D16 / Step 4 rename-over. go-fuse inode identity vs "target keeps its oid".**
After `rename(tmp, file)` go-fuse moves the source Inode to `file` and forgets the target inode; a node with the oid baked in now carries the deleted source oid. Open handles on the old target must keep the old body.
Fix: nodes resolve oid through a mutable mapping updated on rename-over (source node adopts target oid), or workspace aliases. Test in the gedit scenario: after save, stat/reopen/write/second save all hit the target oid.

**R10. Major. Step 4 reducer / editor tests. vim backup pattern can't reduce to one modify.**
rename(file→file~), create(file), unlink(file~): per-oid reduction gives `delete T` + `create N`.
Fix: path-reuse rule: if a batch deletes X and creates file Y whose final path equals X's pre-batch path, Y adopts X's oid → `modify X`. Interacts with R9. Or drop the assertion and accept trash noise. Arbiter to decide.

**R11. Major. Step 5 batching. 200-op cap and per-op `not_before` break folder coalescing.**
In `rm -r` of 400 files, child deletes become ready before the rmdir; `ReadyOps(limit 200)` can take 200 child deletes without the folder delete. Batches aren't stated to run in order.
Fix: readiness per subtree (not ready while any op on an ancestor arrived within the hold window); never split a delete from a pending ancestor delete; 200 cap counts net ops after reduction; batches strictly sequential, levels parallel within a batch. Test with batch size 10 and 25 files.

**R12. Major. Step 5 idempotency relies on `attempts > 0` and `files.list`.**
Crash after Drive accepts a create but before `attempts` persists → duplicate. `files.list` isn't guaranteed consistent with recent creates. `journal.Record` has the same issue.
Fix: pre-allocate IDs with `files.generateIds` (allowed with `drive.file`), store in the persisted batch, create with `id=`. Replay gets 409 → success + `Get`. Use for tree objects, trash uploads, commits, device records, packs. Fake must implement `generateIds` and 409 on duplicate. Keep `FindByProperties` only for rebuild.

**R13. Major. Step 5 resumable uploads. Snapshot must survive `kill -9`.**
Batch stores session URI and offset but not the snapshot path; `cleanup` implies temp file; resume would send bytes from a different file.
Fix: deterministic `cache/snapshots/<batch>/<oid>`, fsynced before session start, recorded in batch, removed only after commit. Keep MD5 of the snapshot to verify on resume.

**R14. Major. Step 6 page-token ordering. Lost-update bug.**
Fix: at join/rebuild fetch `StartPageToken` **before** the listing. When polling, save the new token in the same SQLite transaction as that page's commits. Page through `nextPageToken` to `newStartPageToken`. Request `fields` for parents and appProperties. Test: a commit written on A mid-B-join listing is seen by B.

**R15. Major. Step 6 rebuild and D7. Duplicate Drive files with the same oid unresolved.**
Crash between new-version upload and old deletion, D19 losers on dead devices, and uncommitted uploads leave duplicates.
Fix: the log is authoritative: each oid's state is the newest applied op. Drive files whose (oid, ver)/Drive ID aren't referenced by an applied commit are orphans: ignored, logged, GC'd by any device after a grace period. D19 cleanup doable by any device via a deterministic generateIds'd trash target in the winner's commit.

**R16. Major. Step 6 LWW / D19. No conflict matrix; one version per object loses edits.**
A newer rename on A discards an older content modify on B. Missing: concurrent same-name create, concurrent folder-move cycles, modify vs delete, move into deleted folder, clock skew.
Fix: two LWW registers per object: content and placement. Decision table: name collision → older renamed `name (conflict <device>).ext` deterministically; cycle → older move loses; newer modify vs older delete → resurrect at last path recreating parents; move into trashed folder → resurrect parent. Clamp/warn on stamps >1 h ahead. One two-device test per row.

**R17. Major. Step 4 / Step 6 open files during remote changes.**
(a) Remote modify while a local writable handle is open: later writes land in an orphaned body. (b) Background download into new path: tree shows new size while new opens read the old body. (c) Stamp assignment time undefined.
Fix: defer remote content ops while a writable handle is open, re-run LWW at Release. Stamp taken at Release. On remote modify, switch atomically to new Drive ID/size/cache path, Partial; existing handles keep their old Entry. `keepCache` from "content version unchanged since last open". On remote move invalidate both parents.

**R18. Major. Step 7 move-out / procfilter. FUSE gives a thread ID; page cache hides reads.**
FUSE `pid` is the calling thread's TID; `/proc/<tid>/comm` is the thread name. With `FOPEN_KEEP_CACHE`, cached files produce no READs.
Fix: resolve TID→TGID via `/proc/<tid>/status`; key detector and procfilter on TGID; read `/proc/<tgid>/comm`. Count "opened read-only by this TGID with keep-cache granted on an unchanged Local file" as a whole read, or track open/release. Test with a multi-threaded helper.

**R19. Minor–major. Step 7 / reducer / D15. Deleting a Dirty file trashes stale content.**
`modify + delete → delete` trashes the previous Drive version; the user's latest edits are lost.
Fix: for synced objects, `modify + delete` uploads the latest body straight into `trash/`. `create + delete` inside the window is still dropped.

**R20. Minor. D39 password transport.**
JSON-decoding into a Go string plus net/http buffers creates unwipeable copies; JS can't zero strings.
Fix: `POST /unlock|/create|/join` take raw `application/octet-stream`, capped, `io.ReadFull` into pre-allocated `[]byte`, wiped after Argon2. Document the JS limitation.

**R21. Minor. Step 8 socket.** bind-then-chmod race. Fix: dir 0700, bind under `umask(0177)`, remove stale socket only after a failed connect.

**R22. Minor. D24 `--drive-endpoint`.** Production binary gains an OAuth-skipping flag. Fix: `devdrive` build tag set by `make e2e`.

**R23. Minor. D11 cached `vault.header`.** Stale local copy could hide a newer format. Fix: after token decrypts, fetch Drive header; refuse on vault-ID mismatch or newer version.

**R24. Minor. Package boundaries.** `journal.Ingest([]drive.File)` and `syncer.ChangeSource` returning `drive.ChangesPage` import `drive`; `fusefs.Store` returns `tree.Entry`; `slogtest` collides with stdlib. Fix: consumer-declared structs (`journal.LogFile`, `syncer.RemoteChange`, `fusefs.Attr`) or `model` types; adapters in `daemon`; rename helper to `plaintextcheck`.

**R25. Minor. Dependencies.** Drop `pkg/browser` (stale; use `xdg-open`/Wails `BrowserOpenURL`). Drop drivefake JSON persistence. Generate Wails bindings with `go run …wails@v2.17.0 generate module` instead of hand-written `bridge.ts`. memguard brings frand; acceptable since LockedBuffer isn't a primitive, but state it in the PR.

**R26. Minor. D33 expiry logging.** N devices log N `trash_purge`. Fix: log only on 2xx delete.

**R27. Minor. `.Trash-<uid>`.** `.trashinfo` holds plaintext paths; removing `files/<x>` orphans `info/<x>.trashinfo`. Fix: local-only bodies stored VLT1 in cache; delete matching `.trashinfo`.

**R28. Minor. Step 7 dedup.** Matched trash item purged concurrently → `Copy` 404 → fall back to upload.

**R29. Minor. Pack compaction race.** Listed single deleted by `PackIfDue` → on 404 re-list that device folder and fetch the pack.

**R30. Minor. Free up space.** Plan never says Free is refused for Dirty files or files with an open writer. Add the rule and a test.

**R31. Minor. Unlock offline.** Catch-up must be time-bounded; mount then keep polling.

## Points I agree with (uncontested)

Toolchain Go 1.26 / `toolchain go1.26.8`, staticcheck via `go run`, `testing/synctest`. Module at repo root, `.gitkeep` fix. `internal/model` (D4), `internal/workspace` (D5). HTTP-level fake with in-process RoundTripper, strict `q` parser, fault injection (D6). Hand-written resumable upload and ranged download. D7 (paired with R12, R15). D9, D10, D12, D13. D18 with plaintext-scan test. Budget, classification, retry design. Commit-after-verify, causal hold (D36), pack-then-delete (D42). FUSE test approach with external processes, skip rules, e2e two-instance harness, invalidation outside locks. BurntSushi/toml, bitset, esiqveland/notify, cobra, x/term, adrg/xdg, go-cmp; rejecting uuid, ulid, clockwork, testify, go-systemd. Branch-per-step, `--no-ff`, `make check` per commit, build order per §15, D21, D22, D37, D38, D43, and the fake-vs-real-Google split (§8).
