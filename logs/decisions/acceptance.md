# Acceptance run on real Drive (spec §15)

A is the installed daemon (old build, about 20k files still queued for upload). B is `vaultd-b` at `~/vault-b`, running the current main, joined On demand, mounted at `~/VaultB`. Test data lives only in `/_acceptance`.

## 2026-10-23

| Check | Result |
|---|---|
| No plaintext names or contents under `~/.local/share/vault/` | **Pass.** 30 real file names and 5 text snippets; 0 hits in 4.2 GB. Positive control: the same strings match the plaintext mount. |
| B joins On demand, full tree in < 30 s, nothing downloads | **Partial.** 21,407 files visible, and the cache stayed at 4 KB, so nothing was downloaded. But the tree wasn't there at 30 s: the join returned in 27 s, catch-up hit its 15 s cap downloading one log file, and the rest arrived in the background over a few minutes. To investigate: log catch-up speed. |
| On-demand read is correct | **Pass.** A cloud-only file read on B matched A's MD5; the first read took 3.2 s. |
| Seek without full download (stand-in for the 4 GB video in VLC) | **Pass so far.** On a 188 MB file, the first 1 MB took 2.1 s, then seeks to the end and the middle took 1.0 s and 1.2 s; 20 MB cached. The real 4 GB/VLC check waits until such a file is on Drive. |
| Edit on one device is visible on the other within 30 s; History lane | **Fail under load.** B→A took about 120 s, because A applies incoming commits only between its upload batches (200 files at about 3 files/s). Retest when A is idle. History names `laptop-b` correctly. |
| Move a file out: trash entry, reason moved out, logged | **Pass** (notification: owner to confirm). |
| Move it back: logged as created, no upload, trash entry untouched | **Pass.** Same ciphertext MD5 as the trash copy under a new Drive ID, so it was a server-side copy. |
| Trash Restore and Delete now | **Pass.** Restoring while a different file holds the path gives `name (restored).ext` with identical contents. |
| Orphan GC on a fresh join | **Bug found and fixed** (see build-log, "Fix: orphan GC ran on a device that hadn't caught up"). |
| Unsynced edit on A loses to a newer edit on B; A's version in trash as superseded | **Pass** after the fix below: A's trash shows `/_acceptance/edit.txt  superseded by laptop-b`. Earlier note: A applied B's version (after 112 s, same batch wait as above). A's own version is held by one queued `supersede` op, which is behind the backlog (A's queue: 19k create, 17k modify, 38k attr ops). Recheck once A drains. |

## 2026-10-23, A on the new build and idle

| Check | Result |
|---|---|
| Edit on one device visible on the other within 30 s | **Pass.** A→B 23 s, B→A 21 s. |
| Stuck "1 file not backed up" and restore on A | **Bug found and fixed** (build-log, "Fix: a trashed file stayed not backed up"). After installing the fix, A is `All backed up` and downloaded B's test files. |
