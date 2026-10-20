# Step 2: drive, drivefake, tools/drivefake

Branch `feat/02-drive`. These are the choices I made where the plan and arbitration left room, and every place I departed from them.

## Dependencies

- `google.golang.org/api v0.305.0` (drive/v3), `golang.org/x/time v0.16.0` (rate) and `github.com/cenkalti/backoff/v5 v5.0.3`, all from the plan's table. Nothing else was added.
- drive/v3 pulls in `cloud.google.com/go/auth`, grpc and otel as indirect dependencies. There's no way to avoid them while using drive/v3. `go mod tidy` also moved `golang.org/x/sync` to v0.24.0 and made `golang.org/x/crypto` a direct requirement, which it already was in practice.

## API shape compared with the plan

- `NewClient(ctx, Config, Deps)` replaces `NewClient(ctx, cfg, logger)`. Per plan §3, `Deps` holds the `*http.Client`, the `TokenRefresher`, the `*slog.Logger` and the `now` func, so `HTTPClient` moved out of `Config`.
- `Config.RangeSize` is gone. Splitting one large file into 4 parallel 8 MiB ranges is a job for the download pool (syncer/cache). The client provides `DownloadRange`, which that pool will call.
- I added `CreateFolder(ctx, NewFile)`. Tree folders need a create with a pre-generated ID (R12). `EnsureFolder` looks a folder up by name, which is right for `Vault/`, `tree/`, `log/` and the like, but not for encrypted tree folders. If `EnsureFolder` gets a pre-ID and hits a 409, it falls back to `Get`.
- `CreateSmallFile` takes an `io.ReaderAt` plus a size instead of an `io.Reader`, so each retry reads the body again from offset 0.
- The plan's `File` and `Change` types aren't used. The client returns `model.RemoteFile`, `model.RemoteChange` and `model.ChangesPage`, per arbitration Step 2.1.
- Extra exports: an `*APIError` (status, reason, message) whose `Is` maps to the sentinels, plus `ErrRateLimited`, `ErrPermissionDenied` and `ErrUnauthorized`. `Client.Budget()` exposes the current bucket rates, which a future `/status` can show.

## Budget

- Recovery is multiplicative: each full error-free minute multiplies the current rate by 1.1, capped at the ceiling. A new penalty restarts the minute. The spec's "grows back 10 percent" could also mean 10% of the ceiling. I picked compound growth.
- A bucket never drops below 1/16 of its ceiling. The spec says nothing about a floor, but without one, a burst of concurrent 429s would halve the rate again and again and push it toward zero.
- Halving changes the rate only, not the burst.
- What each bucket covers:
  - Reads: `GenerateIDs`, `Get`, `ListChildren`, `FindByProperties`, `StartPageToken`, `Changes`.
  - Metadata writes: every create (folder, multipart and the resumable session start), `Copy`, `Update` and `Delete`.
  - No cap: upload chunk PUTs, status queries and downloads. These still go through the retry policy, but a rate-limit answer on them doesn't penalise either bucket.

## Retries

- I wrote a custom `backoff.BackOff` for full jitter: uniform in [0, min(64 s, 1 s·2^n)]. It runs with `WithMaxTries(8)` and `WithMaxElapsedTime(0)`, so the library's default 15-minute elapsed-time cutoff doesn't apply.
- Retried: a rate-limit 403, 429 and 5xx. **Transport errors are retried too**, meaning connection resets and short bodies. Without that, an upload or download couldn't resume after the network drops (acceptance: "pulling the network mid-sync"). Context cancellation is never retried.
- 401: the client calls `TokenRefresher.RefreshToken` once per call, then retries after the normal jittered delay instead of at once, which keeps the backoff library's semantics simple. If no refresher is configured, a 401 fails the call immediately.
- `Retry-After` is ignored, because the spec asks for full jitter.

## Uploads and downloads

- Multipart goes through drive/v3 `Media(..., googleapi.ChunkSize(0))`. That forces a single multipart request with no retries inside the library, so our own budget and retry policy stay in charge.
- `option.WithLogger(discard)` is set on the drive/v3 service, because the library's debug logger would dump request bodies, and those carry names.
- `ChunkSize` must be a positive multiple of 256 KiB, which is Drive's rule for non-final chunks. `NewClient` rejects anything else. Tests use 256 KiB chunks.
- `StartResumableUpload` refuses a size of 0. Empty bodies go through `CreateSmallFile`.
- `ResumeUpload` always starts with `bytes */N`, and asks again after any failed chunk. If a 308 confirms no new bytes, that counts as a retryable error, which caps a server that stops making progress at 8 attempts instead of looping forever. Both 404 and 410 become `ErrUploadSessionExpired`.
- `DownloadRange` writes at the absolute file offsets in the `io.WriterAt`. After a cut, `DownloadRange` and `DownloadAll` both resume from the last byte written.
- `ErrPageTokenExpired` covers both a 404 and a 400 with reason `invalid`. A 400 `invalid` could in principle come from some other bad parameter, but the client only ever sends fixed parameters.

## drivefake

- One mutex is held for the whole of each request, so requests are serialised. That keeps the fake simple and deterministic.
- Wire JSON uses the drive/v3 structs, so the encoding matches what the client decodes.
- `fields` partial responses are implemented, using Drive's default masks when no `fields` parameter is sent. A field missing from the client's mask therefore really is absent from the response. I checked this by mutation: removing `md5Checksum` from the mask makes a test fail.
- The fake is strict in the same way Drive is, so client bugs fail loudly:
  - A client-supplied ID must come from `generateIds`. An ID that already exists gets 409.
  - An upload chunk must start exactly at the persisted byte count (this is the "no byte re-sent" proof).
  - Non-final chunks must be 256 KiB aligned.
  - Multipart bodies are capped at 5 MiB.
  - The `q` parser accepts only the listed clauses.
- API additions beyond the plan:
  - `ExpireUploadSessions()`, for the expired-session test.
  - `Endpoint()` and `UploadEndpoint()`, the in-process URLs that pair with `HTTPClient()`.
  - `KindOf(*http.Request)`, which tests use in `Fault.Match`.
  - `RequestRecord` also carries `Path` and `Offset`. `Offset` is the Content-Range or Range start, or -1.
- Fault semantics:
  - `Times < 1` counts as 1, and `Status` 0 means 503.
  - `AfterBytes` also cuts downloads: the body is truncated under a full Content-Length.
- `CorruptContent` flips a byte but leaves `md5Checksum` alone. That models silent damage, which only `verify --deep` can find.
- Quota: going over the per-minute limit returns 403 `userRateLimitExceeded`, and going over the create rate (create, copy, upload start) returns 429 `rateLimitExceeded`.
- Deleting a folder deletes its descendants, as Drive does.
- There's no persistence (R25).

## tools/drivefake

- `//go:build devdrive`. It takes `-listen 127.0.0.1:0`, prints the two endpoints and shuts down cleanly on SIGINT or SIGTERM. The `--drive-endpoint` flag for vaultd (R22) belongs to step 8, since `cmd/vaultd` doesn't exist yet.
- `go vet ./...` and staticcheck skip the tagged package by default. I ran both with `-tags devdrive` by hand. `make check` doesn't do this; step 8 may want to add it.

## Tests

- Time-dependent tests use `testing/synctest` with the fake's in-process transport. One checks that 16 creates under the default budget take exactly 2 s of virtual time. Another checks that 3 retries under the real 1 s–64 s schedule finish within 7 s. Budget recovery uses an injected clock.
- Everything else uses a 1 ms–4 ms backoff schedule. `go test -race ./internal/drive/...` takes about 3 s.
- I also checked by mutation that dropping the initial `bytes */N` status query makes three upload tests fail.
- Auth (`feat/02b-auth`) isn't on this branch.
