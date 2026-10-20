# Vault v1: Implementation Plan (Planner)

I read the whole spec (600 lines) and checked the environment. Everything below is a proposal for the critic and the arbiter to rule on. Each decision carries its reason.

## 0. Facts I checked and what follows from them

| Fact | Consequence |
|---|---|
| The repo is already a git repo on `main` (commit `aa1fd67`). It holds the spec, a `.gitignore` that lists `panel/frontend/node_modules/` and `panel/frontend/dist/` at repo-root paths, and `logs/decisions/` with a README naming `01-plan.md`, `02-review.md`, `03-arbitration.md`, `build-log.md` | The module root is the **repo root**. The `.gitignore` already assumes this. The coordinator should save this plan as `logs/decisions/01-plan.md`. |
| The latest versions of x/crypto (v0.58.0), x/sync (v0.24.0), x/time (v0.16.0), x/oauth2 (v0.37.0), x/term (v0.47.0), modernc.org/sqlite (v1.60.1), google.golang.org/api (v0.305.0) and staticcheck v0.8.2 all declare `go 1.26.0`. Wails v2.17.0 needs 1.25. klauspost/compress v1.20.3 needs 1.25 | Go 1.22 can't use current dependencies. |
| Toolchains `go1.26.5`, `go1.26.7` and `go1.26.8` are already in the module cache. The newest release is 1.27.3 | Pin `toolchain go1.26.8`. It needs no download, and 1.26 is still a supported major. |
| The installed staticcheck is 2024.1.1, built for go1.22 | It can't analyse go1.26 code. Run `go run honnef.co/go/tools/cmd/staticcheck@v0.8.2` from the Makefile instead. |
| `webkit2gtk-4.1` 2.52.6, `libsoup-3.0`, gtk3, gcc and pkg-config are installed. DISPLAY and WAYLAND are set | The Wails v2 panel can be built **without the Wails CLI**: `go build -tags desktop,production,webkit2_41 ./panel`. This is realistic. |
| `dbus-daemon`, `notify-send` and a session bus are present. `/dev/fuse` and `fusermount3` are present. uid is 1000 | D-Bus and FUSE tests can run here. |
| The memlock ulimit is about 1.9 GiB. A 4 GiB swap file is active. The home directory is on ext4 | `mlockall` is feasible in this environment. ext4 has no reflink, so cache snapshots fall back to copying. |

---

## 1. Toolchain, module, layout

- **Module root:** the repo root (`/home/revant-sinha/Desktop/vdeskproject`). The `vault/` in spec §14 is the repo name, not a subfolder; nesting it would only add a path level, and `.gitignore` already assumes root-level paths.
- **Module path:** `github.com/cosmoboy/vault`. The spec author is @Cosmoboy; this is a guess (open decision D2), and fixing it later is a one-line `go mod edit` plus `sed`.
- **go.mod:** `go 1.26.0` and `toolchain go1.26.8`. GOTOOLCHAIN=auto selects it from the cache.
  - Rejected alternative: pinning old dependencies that still accept go1.22 (for example x/crypto ≤v0.31, sqlite ≤v1.3x). That means old crypto and SQLite code. Go 1.22 is also end-of-life with unpatched stdlib CVEs in net/http and crypto/tls. This project is a security tool.
  - Bonus: go1.26 gives us `testing/synctest` (GA since 1.25) for virtual time in tests, the `iter` package, and `os.Root`.
- **Binaries:** `cmd/vaultd`, `cmd/vault` and `panel/` (output name `vault-panel`). `vaultd` and `vault` must build with `CGO_ENABLED=0`. Only the panel uses cgo.
- **Layout:** spec §14, plus these additions, each justified in its own open decision:
  - `internal/model`: shared value types (D4).
  - `internal/workspace`: the local authoritative view that implements the fusefs store (D5).
  - `internal/drive/drivefake`: the fake Drive.
  - `internal/devicetest`: the test-only multi-device harness.
  - `tools/drivefake`: a dev server binary for manual and e2e runs (D24).
  - `test/e2e`: process-level tests under the `e2e` build tag.
  - `panel/layout`: pure-Go commit-graph lane layout (D30).
- **Makefile targets:**
  - `fmt-check` (`gofmt -l`)
  - `vet`
  - `lint` (`go run honnef.co/go/tools/cmd/staticcheck@v0.8.2 ./...`)
  - `test` (`go test -race ./...`)
  - `check` (all of the above)
  - `build` (`vaultd` and `vault` with CGO off)
  - `panel` (`npm ci && npm run build` in `panel/frontend`, then the tagged `go build`)
  - `e2e` (`go test -tags e2e ./test/e2e/...`)
- **Panel and `./...`:** Wails v2 compiles a runtime stub without the `desktop` and `production` tags, so `go vet ./...` and `go test ./...` work without building the frontend. They still need cgo and webkit headers, which are present.
- **Embedded assets on a fresh clone:** `//go:embed all:frontend/dist` would fail when `dist` doesn't exist yet. Change `.gitignore` to `panel/frontend/dist/*` and `!panel/frontend/dist/.gitkeep`, and commit the `.gitkeep`.

## 2. Dependencies

Baseline from spec §14, pinned at the latest versions:

| Package | Version | Use |
|---|---|---|
| github.com/hanwen/go-fuse/v2 | v2.12.0 | FUSE (`fs` package) |
| golang.org/x/crypto | v0.58.0 | argon2, chacha20poly1305, hkdf |
| google.golang.org/api | v0.305.0 | drive/v3 metadata calls |
| golang.org/x/oauth2 | v0.37.0 | OAuth, PKCE (`GenerateVerifier`, `S256ChallengeOption`) |
| modernc.org/sqlite | v1.60.1 | index |
| golang.org/x/sync | v0.24.0 | errgroup |
| golang.org/x/time | v0.16.0 | rate |
| github.com/klauspost/compress | v1.20.3 | zstd |
| github.com/godbus/dbus/v5 | v5.2.4 | D-Bus (indirect, through esiqveland/notify) |
| github.com/wailsapp/wails/v2 | v2.17.0 | panel (released 2 days ago; fall back to v2.16.0 if it misbehaves) |
| golang.org/x/sys | v0.49.0 | unix: Prctl, Setrlimit, Mlockall, GetsockoptUcred, FICLONE (already an indirect dependency of go-fuse) |

Extra packages, per the coordinator's "use well-established packages" rule. Every one has to be justified in its PR:

| Package | Version | Reason |
|---|---|---|
| github.com/BurntSushi/toml | v1.6.0 | The de facto Go TOML library. `MetaData.Undecoded()` lets the config package reject unknown keys. |
| github.com/awnumar/memguard | v0.23.0 | Mature library for mlocked, guard-paged, canaried and wiped key buffers. `internal/secret` becomes a thin wrapper over `LockedBuffer`, so we don't hand-roll mlock or zeroing. It has process-global state of its own, but that is library-owned, not ours. |
| github.com/cenkalti/backoff/v5 | v5.0.3 | Widely used retry loop: Permanent errors, RetryAfter, context cancellation. We plug in a 6-line `BackOff` implementation for the spec's exact full-jitter 1–64 s schedule, because the library's own randomisation isn't full jitter. |
| github.com/bits-and-blooms/bitset | v1.26.0 | Segment-presence bitmaps with `MarshalBinary`. Widely adopted. Plain bitset rather than roaring, because the bitmaps are dense and small (a 4 GB file is about 64k bits, 8 KB). |
| github.com/esiqveland/notify | v0.14.0 | Typed `org.freedesktop.Notifications` client on top of godbus. Saves hand-writing the D-Bus signature. |
| github.com/tmaxmax/go-sse | v0.11.0 | SSE server and client, used for `GET /events` and by the panel's Go side. Moderate adoption. Fallback if rejected: the stdlib server (`http.Flusher`) plus about 40 lines of client parsing. |
| github.com/spf13/cobra | v1.10.2 | CLI subcommands with a persistent `--home` flag. Ubiquitous. |
| golang.org/x/term | v0.47.0 | No-echo password entry for `vault unlock`, `create` and `join`. |
| github.com/adrg/xdg | v0.5.3 | XDG data, config, state and runtime directories for the default (non-`--home`) layout. |
| github.com/pkg/browser | 2024-01-02 pseudo-version | Opens the OAuth URL in the CLI client. Widely used. |
| github.com/google/go-cmp | v0.7.0 | Readable struct diffs in tests. Maintained by the Go team. I chose it over testify so tests stay plain `if` checks with `cmp.Diff`. |

Considered and rejected:

- **oklog/ulid, google/uuid:** IDs must be random and must not reveal time order. `crypto/rand` plus hex is a single line.
- **HLC libraries:** there is no dominant, maintained Go HLC library. The clock is about 50 lines of logic and tested heavily.
- **jonboulle/clockwork:** `testing/synctest` from the stdlib covers virtual time.
- **go-systemd:** `Type=simple` needs no sd_notify.
- **testify:** see go-cmp above.

Dev tools, run via `go run pkg@version` and kept out of go.mod: staticcheck v0.8.2. Frontend: `vite` and `typescript` only, pinned in `package-lock.json`. No UI framework, per the spec.

## 3. Cross-cutting conventions

- **Dependency injection:** every constructor takes a `Deps` struct and a `Config` struct. Time arrives as `now func() time.Time`, randomness as `io.Reader`. There are no package-level vars except sentinel errors and constants.
- **Errors:** wrapped with `%w`. Each package exports its sentinels: `crypto.ErrWrongPassword`, `drive.ErrNotFound`, `drive.ErrPageTokenExpired`, `drive.ErrUploadSessionExpired`, `vaultfile.ErrTampered`.
- **Goroutines:** owned by a `Run(ctx) error` method. Fan-out uses `errgroup` with `SetLimit`. All channels are buffered with a fixed capacity.
- **Logging:** a `*slog.Logger` is injected. Only oids, Drive IDs and counts are logged, never names. A test helper, `slogtest.NoPlaintext(t, buf, secrets...)`, asserts this in the tests that matter.
- **Timing:** every duration (quiet period, poll intervals, the 10 s move-out window, the 3 s coalescing window, backoff) lives in a config struct. Unit tests use `testing/synctest` for time-dependent logic: batcher, poller, adaptive budget, backoff, trash expiry. Synctest works because the fake Drive is reached through an in-process `http.RoundTripper` with no sockets (§5 Step 2).
- **Git workflow:**
  - One branch per build step, split further for large steps.
  - Small commits, each passing `make check`, with messages like `crypto: derive subkeys with HKDF`.
  - Merge to `main` with `git merge --no-ff` so each feature shows as one merge in history.
  - Problems that come up during the build go into `logs/decisions/build-log.md`.

## 4. Shared vocabulary: `internal/model` (D4)

```go
type ObjectID string            // 32 hex chars, random 16 bytes; RootID = "root"
type DeviceID string            // "d-" + 16 hex
type Kind uint8                 // KindFile, KindFolder
type LocalState uint8           // CloudOnly, Partial, Local, Dirty
type Stamp struct { WallMillis int64; Counter uint16; Device DeviceID }
func (s Stamp) String() string  // "%013d.%04d.%s", so lexical order == HLC order (stored as TEXT in SQLite)
func ParseStamp(string) (Stamp, error)
func (s Stamp) After(o Stamp) bool
func NewObjectID(random io.Reader) (ObjectID, error)
```

These are value types only, with no behaviour beyond parse, compare and format. Without this package we would get import cycles: index, tree, cache, workspace, syncer and journal all need ObjectID and Stamp. The HLC *generator* stays in `journal`, as the spec says.

---

## 5. Build steps

### Step 0: `chore/00-scaffold`

Commits:
1. go.mod with the toolchain pin.
2. Makefile.
3. `.gitignore` dist exception and `.gitkeep`.
4. `internal/model` with its tests.
5. A short README that says how to run the checks.

Gate: `make check` is green.

### Step 1: `feat/01-crypto-vaultfile` (packages `secret`, `crypto`, `vaultfile`)

**secret**

```go
type Key struct{ buf *memguard.LockedBuffer }
func NewKey(material []byte) (*Key, error)     // copies into locked memory, wipes material
func RandomKey(size int) (*Key, error)
func (k *Key) Bytes() []byte                    // view; invalid after Destroy
func (k *Key) Destroy()
func Wipe(b []byte)
func HardenProcess() (Report, error)            // PR_SET_DUMPABLE=0, RLIMIT_CORE=0, best-effort mlockall(MCL_CURRENT|MCL_FUTURE)
```

`mlockall` covers what memguard can't reach: Argon2's 256 MiB working buffer, and the key schedule that chacha20poly1305 copies onto the Go heap. If it fails, vaultd logs a warning and `/status` reports `keysMaySwap: true` (D29).

**crypto**

```go
type KDFParams struct{ Time, MemoryKiB uint32; Threads uint8 }
func DefaultKDFParams() KDFParams                    // 3, 256*1024, 4
type VaultHeader struct{ FormatVersion int; VaultID [16]byte; KDF KDFParams; Salt [32]byte; WrappedMasterKey []byte }
func MarshalVaultHeader(VaultHeader) ([]byte, error)  // JSON, base64 fields (D11)
func ParseVaultHeader([]byte) (VaultHeader, error)    // ErrUnsupportedFormat if newer
type Keys struct{ VaultID [16]byte; Name, Content, Log, Token, IndexSeal, IndexMAC *secret.Key }
func (k *Keys) Destroy()
func CreateVault(password []byte, params KDFParams, random io.Reader) (VaultHeader, *Keys, error)
func UnlockVault(h VaultHeader, password []byte) (*Keys, error)   // ErrWrongPassword
func Seal(key *secret.Key, plaintext, additionalData []byte, random io.Reader) ([]byte, error) // nonce||ct||tag
func Unseal(key *secret.Key, sealed, additionalData []byte) ([]byte, error)
func WrapKey(wrapping, key *secret.Key, additionalData []byte, random io.Reader) ([]byte, error)
func UnwrapKey(wrapping *secret.Key, wrapped, additionalData []byte) (*secret.Key, error)
type NameCipher struct{ ... }
func NewNameCipher(key *secret.Key, vaultID [16]byte, random io.Reader) NameCipher
func (c NameCipher) EncryptName(name string) (string, error)   // base64url, no padding
func (c NameCipher) DecryptName(encoded string) (string, error)
func ContentMAC(key *secret.Key, plaintextSHA256 [32]byte) []byte   // HMAC-SHA256 (D13)
```

Key details:
- **Master-key wrap:** associated data is `"vault-header/v1" || vaultID`.
- **HKDF:** salt = vaultID. The info labels are `vault/v1/name`, `content`, `log`, `token` and `index`. The index subkey is expanded again into `index/seal` and `index/mac`, so one key is never used for both an AEAD and an HMAC (D12).
- **Name associated data:** vaultID only, so a move doesn't re-encrypt the name (D14).

**vaultfile**

```go
const SegmentPlaintextSize = 64 << 10; SegmentOverhead = 40; HeaderSize = 128
type BodyID [16]byte
type Header struct{ Version uint16; BodyID BodyID; WrappedFileKey []byte }
type Storage interface{ io.ReaderAt; io.WriterAt; Truncate(int64) error }
func NewBody(s Storage, contentKey *secret.Key, random io.Reader) (*Body, error)
func OpenBody(s Storage, contentKey *secret.Key, plaintextSize int64) (*Body, error)
func (b *Body) ReadAt(p []byte, off int64) (int, error)
func (b *Body) WriteAt(p []byte, off int64) (int, error)   // read-modify-write of edge segments, fresh nonce per rewrite
func (b *Body) Truncate(size int64) error                   // re-seals the new final segment with final=true
func (b *Body) Size() int64
func (b *Body) Header() Header
func (b *Body) Close()                                     // destroys file key
func CiphertextSize(plaintext int64) int64
func PlaintextSize(ciphertext int64) (int64, error)
func SegmentOffset(index int64) int64                       // HeaderSize + index*(65536+40)
func SegmentSpan(off, length int64) (first, last int64)
func VerifyBody(r io.ReaderAt, ciphertextSize int64, contentKey *secret.Key) ([32]byte, error) // deep verify: decrypt all, return SHA-256
```

Format:
- **Header (128 bytes, fixed):** magic `VLT1` (4), version u16, BodyID (16), wrapped file key (24 + 32 + 16 = 72), zero padding (D10).
- **Segment associated data:** `BodyID || u64be(index) || finalFlag`.
- **Segment count:** a body always has at least one segment. A zero-length file is a single 40-byte final segment, so truncation to zero is detectable (D9).
- **Body ID:** separate from the oid. It is regenerated together with the file key on create and on `O_TRUNC` to zero. In-place edits keep the key and use fresh nonces (D8).

Commits (about 8): secret buffers; process hardening; KDF and header; subkeys; seal and wrap; names; segment seal and open; Body random access; VerifyBody.

Tests that gate the merge:
- Round trips at sizes 0, 1, 65535, 65536, 65537, 3×64 KiB, and random sizes.
- WriteAt and ReadAt across segment edges, using a fuzz test against a plain `[]byte` model (`go test -fuzz` seed corpus committed).
- Tampering fails: flip a bit, swap two segments, move a segment between bodies, truncate the last segment, append a segment, replay an old segment version. The last one is expected to pass; it is a documented limitation (D8).
- Wrong password gives `ErrWrongPassword`. A header with a newer version gives `ErrUnsupportedFormat`.
- A test with the default KDF params, skipped under `-short`. Every other test uses small params.
- Destroyed keys panic or zero on use. `HardenProcess` is checked via `/proc/self/status` (`VmLck`) and `PR_GET_DUMPABLE`.

### Step 2: `feat/02-drive` and `feat/02b-auth` (packages `drive`, `drive/drivefake`, `auth`)

**drive.Client**

Metadata calls go through drive/v3 configured with `option.WithEndpoint` and `option.WithHTTPClient`. Resumable upload and ranged download are hand-written over `*http.Client`. They have to be: drive/v3 doesn't expose the upload session URI, and we must persist it to resume after `kill -9`.

```go
type File struct{ ID, Name, MD5 string; Parents []string; Size int64; ModifiedTime time.Time; Folder bool; Properties map[string]string }
type NewFile struct{ Name, ParentID string; Folder bool; ModifiedTime time.Time; Properties map[string]string }
type UploadSession struct{ URI string; Size int64 }
type Change struct{ FileID string; Removed bool; File *File }
type ChangesPage struct{ Changes []Change; NextPageToken, NewStartPageToken string }
type Config struct{ Endpoint, UploadEndpoint string; HTTPClient *http.Client; Budget BudgetConfig; Retry RetryConfig; ChunkSize int64 /*16 MiB*/; RangeSize int64 /*8 MiB*/ }
func NewClient(ctx context.Context, cfg Config, logger *slog.Logger) (*Client, error)
func (c *Client) EnsureFolder(ctx, parentID, name string, props map[string]string) (File, error)
func (c *Client) CreateSmallFile(ctx, f NewFile, body io.Reader, size int64) (File, error)        // multipart, <=5 MiB
func (c *Client) StartResumableUpload(ctx, f NewFile, size int64) (UploadSession, error)
func (c *Client) ResumeUpload(ctx, s UploadSession, body io.ReaderAt, onConfirmed func(offset int64)) (File, error) // queries "bytes */N" first
func (c *Client) Copy(ctx, srcID string, f NewFile) (File, error)
func (c *Client) Update(ctx, id string, change MetadataChange) (File, error) // name, add/remove parents, properties, modifiedTime in one call
func (c *Client) Delete(ctx, id string) error                                  // 404 => nil
func (c *Client) Get(ctx, id string) (File, error)
func (c *Client) FindByProperties(ctx, parentID string, props map[string]string) ([]File, error)
func (c *Client) ListChildren(ctx, parentID string) ([]File, error)            // pages of 1000
func (c *Client) DownloadRange(ctx, id string, off, length int64, w io.WriterAt) error
func (c *Client) DownloadAll(ctx, id string, w io.Writer) error
func (c *Client) StartPageToken(ctx) (string, error)
func (c *Client) Changes(ctx, token string) (ChangesPage, error)               // ErrPageTokenExpired on 404/400 invalid token
```

**Budget** (`budget.go`): two `rate.Limiter` buckets.
- Metadata writes: 3/s, burst 10. File creates (including multipart) count here.
- Reads: 10/s, burst 30.
- On a rate-limit 403 (reason `userRateLimitExceeded` or `rateLimitExceeded`) or a 429, the bucket's rate halves. It grows back 10% per error-free minute, up to its ceiling. Time comes from the injected `now`.
- Classification is `classifyResponse(status, googleapiErrorReason) retryClass`. A 403 for permissions isn't retried. A 401 triggers one forced token refresh through an injected `TokenRefresher` interface.
- Retries use `backoff.Retry` with full jitter from 1 s to 64 s, and at most 8 attempts per call. After that the error surfaces to the syncer, which reschedules through `pending_ops.next_attempt`.

**drivefake** (D6) is an `http.Handler` implementing the subset of the Drive v3 REST API the client uses:
- `files.create` (metadata, multipart, resumable with 308 and `Range`, and status queries)
- `files.update`, including `addParents` and `removeParents`
- `files.get`, including `alt=media` with `Range`
- `files.list` with a strict `q` parser: `'x' in parents`, `name = `, `trashed = false`, `appProperties has { key= and value= }`, `mimeType =`, joined with `and`. Any other query returns 400, so client drift shows up.
- `files.delete`, `files.copy`
- `changes.getStartPageToken`, `changes.list`
- md5Checksum on finalised uploads

```go
func New(opts ...Option) *Server                 // WithNow(func() time.Time), WithQuota(perMinute int, createsPerSecond float64, burst int)
func (s *Server) Handler() http.Handler
func (s *Server) HTTPClient() *http.Client        // in-process RoundTripper: no sockets, synctest-safe
func (s *Server) InjectFault(f Fault)             // Fault{Match func(*http.Request) bool; Status int; Reason string; Times int; AfterBytes int64 /*cut upload mid-stream*/}
func (s *Server) Requests() []RequestRecord        // method, kind (metadata|upload|download|list|changes), status
func (s *Server) File(id string) (drive.File, []byte, bool)
func (s *Server) CorruptContent(id string, offset int64)
func (s *Server) ExpirePageTokens()
```

A quota model returns real-looking 403 and 429 responses when the sustained create rate or the per-minute limit is exceeded. `tools/drivefake` wraps it in an `httptest`-style listener, with optional JSON state persistence, for manual and e2e runs.

**auth**

```go
type ClientCredentials struct{ ClientID, ClientSecret string }
func LoadClientCredentials(overridePath string, embedded []byte) (ClientCredentials, error) // ErrPlaceholderCredentials
type Flow struct{ ... }    // NewFlow(cfg *oauth2.Config, listen func() (net.Listener, error))
func (f *Flow) Begin(ctx) (authURL string, wait func(context.Context) (*oauth2.Token, error), err error) // PKCE S256, loopback 127.0.0.1:0, state check
type TokenStore struct{ ... } // NewTokenStore(path string, key *secret.Key)
func (s *TokenStore) Save(*oauth2.Token) error     // seal with token subkey, 0600, atomic rename
func (s *TokenStore) Load() (*oauth2.Token, error)
func (s *TokenStore) PersistingSource(base oauth2.TokenSource) oauth2.TokenSource // re-saves rotated tokens
```

`internal/auth/oauth_client.json` is committed as a placeholder and compiled in with `go:embed`. The `oauth_client_file` setting in config.toml overrides it (D28).

Commits (about 12): types and errors; fake core (files CRUD and the q parser); fake uploads; fake changes; fake quota and faults; budget; retry classification; metadata methods; resumable upload; ranged download; changes; auth flow; token store.

Tests that gate the merge:
- Each client method against the fake.
- Injected 403, 429 and 5xx are retried. A permission 403 isn't. A 404 on delete counts as success. A 401 refreshes the token exactly once.
- Under synctest: the budget halves on an error and recovers 10% per minute. Backoff stays within [0, cap].
- A resumable upload cut at byte N (`AfterBytes`) resumes from the confirmed offset. The fake records that no byte below the offset was re-sent. An expired session gives `ErrUploadSessionExpired`.
- A Range download of the middle of a file works. Changes paging works, including a token-expired case.
- Auth: a fake token endpoint plus a "browser" that GETs the redirect URL checks PKCE, state mismatch and timeout. The token store round-trips, and a wrong key fails.

### Step 3: `feat/03-index-tree-cache` (packages `index`, `tree`, `cache`)

**index**: SQLite via modernc, WAL, `synchronous=NORMAL` (D40), `busy_timeout`, a single writer connection. Migrations are embedded `migrations/NNN_*.sql`; the schema version is kept in `kv`.

Tables:
- `objects`: `id, parent_id, kind, enc_name, drive_id, size, mtime, executable, content_mac, body_md5, version(TEXT stamp), local_state, trash_id NULL, local_only`
- `segments`: `object_id, bitmap BLOB`
- `pending_ops`: `seq, object_id, kind, payload(sealed with index/seal), stamp, not_before, attempts, next_attempt, batch_id NULL`
- `outgoing_batches`: `id, commit_id, net_ops(sealed), state`. This table is extra (D15).
- `commits`: `id, device, clock, parents(JSON of ids), sealed BLOB, applied, own`
- `trash`: `id, object_id, enc_path(sealed), deleted_at, device, reason, size, file_count, content_mac, drive_id`
- `kv`

Rule: every column that could hold a name or path is sealed or is the Drive encrypted name (D18).

```go
func Open(ctx context.Context, path string) (*Store, error)
func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error
func (s *Store) View(ctx context.Context, fn func(*Tx) error) error
// Tx: PutObject, Object, DeleteObject, ChildrenOf, AllObjects (iter.Seq2), ObjectsByContentMAC,
//     AppendOp, ReadyOps(now, limit), AssignBatch, DeleteOps, PutBatch, OpenBatches,
//     SegmentBitmap/PutSegmentBitmap, PutCommit, Commits(beforeClock, limit), UnappliedCommits, MarkApplied,
//     PutTrash, TrashEntries, DeleteTrash, GetKV/SetKV
```

**tree**: map-based, `sync.RWMutex`, returns `Entry` values, never pointers.

```go
type Entry struct{ ID, ParentID model.ObjectID; Name string; Kind model.Kind; Size int64; ModTime time.Time; State model.LocalState; Executable, LocalOnly bool }
func New() *Tree
func Build(ctx context.Context, rows iter.Seq2[EncryptedEntry, error], decryptName func(string) (string, error), workers int) (*Tree, error) // errgroup SetLimit(GOMAXPROCS)
func (t *Tree) Lookup(parent model.ObjectID, name string) (Entry, bool)
func (t *Tree) Get(id model.ObjectID) (Entry, bool)
func (t *Tree) Children(id model.ObjectID) []Entry
func (t *Tree) Insert(e Entry) error
func (t *Tree) Update(e Entry) error
func (t *Tree) Move(id, newParent model.ObjectID, newName string) error
func (t *Tree) Remove(id model.ObjectID) ([]Entry, error)   // returns removed subtree
func (t *Tree) PathOf(id model.ObjectID) (string, error)
func (t *Tree) Resolve(path string) (Entry, bool)
func (t *Tree) Aggregate(id model.ObjectID) (total, local int64)
```

**cache**: `cache/<oid[0:2]>/<oid>`, ciphertext stored sparse.

```go
type SegmentFetcher interface{ FetchRange(ctx context.Context, id model.ObjectID, off, length int64, w io.WriterAt) error } // declared here
type BitmapStore interface{ SegmentBitmap(ctx, id) (*bitset.BitSet, error); PutSegmentBitmap(ctx, id, *bitset.BitSet) error }
func New(dir string, deps Deps) *Cache
func (c *Cache) Create(id model.ObjectID) (*Entry, error)                       // new body, Local
func (c *Cache) Open(ctx context.Context, id model.ObjectID, plaintextSize int64, state model.LocalState) (*Entry, error)
func (e *Entry) ReadAt(ctx context.Context, p []byte, off int64) (int, error)  // fetches missing segments in 128-segment (~8 MiB) aligned runs, header first
func (e *Entry) WriteAt(ctx context.Context, p []byte, off int64) (int, error) // fetches edge segments if absent
func (e *Entry) Truncate(size int64) error
func (e *Entry) Sync() error
func (e *Entry) Complete() bool
func (e *Entry) Close() error
func (c *Cache) Snapshot(id model.ObjectID) (path string, cleanup func(), err error) // FICLONE, else copy (D20)
func (c *Cache) Replace(id model.ObjectID, from string) error                        // rename over; open fds keep old inode (spec §6.3)
func (c *Cache) Free(id model.ObjectID) error
```

Commits (about 10): migrations and schema; objects; pending ops and batches; commits, trash and kv; tree core; tree build; cache entry local; cache sparse fetch and bitmap; snapshot, replace and free.

Tests that gate the merge:
- Migrations are idempotent. Reopen after an unclean close (process killed via a helper subprocess) keeps committed rows.
- `ReadyOps` honours `not_before` and `next_attempt`.
- Tree: rename into own subtree is refused, duplicate name gives `fs.ErrExist`, parallel Build over 10k entries is deterministic, PathOf and Resolve agree.
- Cache: sparse reads with a fake fetcher record the fetched ranges (aligned, header included, no refetch of present segments). The bitmap persists across reopen. Snapshot is isolated from later writes. Replace keeps an old reader working.
- A byte scan of `index.db`, the WAL and the cache after writing known names and contents finds no plaintext.

### Step 4: `feat/04-workspace-fusefs` (packages `workspace`, `fusefs`)

**fusefs** declares what it consumes. Errors use stdlib values (`fs.ErrNotExist`, `fs.ErrExist`, `syscall.Errno`), so fusefs needn't import workspace.

```go
type Caller struct{ PID, UID uint32 }
type Store interface {
    Root() model.ObjectID
    Lookup(parent model.ObjectID, name string) (tree.Entry, error)
    Attr(id model.ObjectID) (tree.Entry, error)
    List(id model.ObjectID) ([]tree.Entry, error)
    Mkdir(ctx context.Context, parent model.ObjectID, name string) (tree.Entry, error)
    Create(ctx context.Context, parent model.ObjectID, name string, mode uint32, c Caller) (tree.Entry, Handle, error)
    Open(ctx context.Context, id model.ObjectID, flags uint32, c Caller) (Handle, bool /*keepCache*/, error)
    Rename(ctx context.Context, parent model.ObjectID, name string, newParent model.ObjectID, newName string, flags uint32) error
    Unlink(ctx context.Context, parent model.ObjectID, name string, c Caller) error
    Rmdir(ctx context.Context, parent model.ObjectID, name string, c Caller) error
    SetAttr(ctx context.Context, id model.ObjectID, change AttrChange) (tree.Entry, error)
    StatFS() (StatFS, error)
}
type Handle interface {
    ReadAt(ctx context.Context, p []byte, off int64) (int, error)
    WriteAt(ctx context.Context, p []byte, off int64) (int, error)
    Sync(ctx context.Context) error
    Release(ctx context.Context) error
}
type Options struct{ MountPoint string; UID, GID uint32; EntryTimeout, AttrTimeout time.Duration }
func Mount(store Store, opts Options, logger *slog.Logger) (*Mount, error) // fsname=vault, subtype=vault, default_permissions
func (m *Mount) Unmount() error
func (m *Mount) InvalidateContent(id model.ObjectID)
func (m *Mount) InvalidateEntry(parent model.ObjectID, name string)
```

- Inode numbers come from a per-session counter mapped to the oid (D37).
- Symlink, hard link and mknod return `EPERM`. Xattr calls return `ENOTSUP`. `RENAME_EXCHANGE` returns `EINVAL`. `RENAME_NOREPLACE` is honoured.
- `chmod` keeps only the executable bit. `utimens` stores mtime and syncs it as metadata (D22).
- `StatFS` reports the cache filesystem's free space (D21).
- The caller PID comes from `fuse.FromContext(ctx)`.

**workspace** implements `fusefs.Store` over tree, index and cache, and later exposes `ApplyRemote*` methods to the syncer.

```go
type StampSource interface{ Next() model.Stamp }                  // journal.Clock later
type DeleteObserver interface{ ObserveDelete(...) }               // trash move-out detector, nil-safe in step 4
func New(deps Deps, cfg Config) (*Workspace, error)               // Config{QuietPeriod 2s, DeleteHold 3s, TrashDirName ".Trash-<uid>"}
```

Recording semantics (D15, D16):
- Metadata mutations (create, mkdir, rename, unlink, rmdir, setattr) write the object row and append a `pending_ops` row in **one** transaction. `not_before` is now + quiet period, or now + 3 s for deletes.
- The first write on a handle marks the object `Dirty`; that is one transaction per open session, not one per `write()`. `Release` of a writable handle appends a `modify` op. On restart, `Dirty` objects with no pending op get a `modify` re-enqueued, which covers a crash while a file was open.
- Rename over an existing file: the target keeps its oid. The source's cache body moves to the target's path. The op is `modify{target, replacedFrom: sourceOID}`.
- `.Trash-<uid>` at the mount root is `local_only`. A rename into `.Trash-<uid>/files/` becomes an immediate vault delete.

The step 4 net-effect reducer is a pure function. It is placed in `syncer` from the start, so step 4 tests can assert net effect:

```go
func ReduceToNetEffect(ops []index.PendingOp, remote func(model.ObjectID) RemoteFacts) []NetOp
```

Commits (about 12): workspace lookup and list; create and mkdir; read and write handles; truncate and setattr; rename; rename-over; unlink and rmdir; local-only desktop trash; reducer (pure); fusefs nodes; fusefs handles and refusals; mount, invalidation and statfs.

Tests that gate the merge:
- Workspace unit tests with no FUSE.
- The reducer table: create+delete gives nothing; create+renames+modifies gives one create at the final location; renames back to the original location give nothing; child deletes inside a deleted folder are subsumed; rename-over gives modify(target) plus a drop of the source.
- The FUSE integration suite (§6).

### Step 5: `feat/05a-syncer-outgoing` then `feat/05b-journal` (packages `syncer`, `journal`)

Journal first in commit order, because the outgoing pipeline ends with a commit. I keep the two branches per the spec's order and merge 05a with a stub `Committer` used only in tests.

**journal**

```go
type Clock struct{ ... }
func NewClock(device model.DeviceID, wall func() time.Time, last model.Stamp, persist func(model.Stamp)) *Clock
func (c *Clock) Next() model.Stamp            // counter overflow at 9999 bumps wall by 1 ms
func (c *Clock) Observe(remote model.Stamp)
type OpKind string // create modify rename move delete moved_out superseded trash_restore trash_purge
type Op struct{ Kind OpKind; ObjectID, ParentID model.ObjectID; Name, Path, From, To string; Folder bool; Size int64; ModTime time.Time;
                ContentMAC []byte; DriveID, DriveName, BodyMD5 string; Version model.Stamp; ReplacedFrom model.ObjectID; Trash *TrashRef }
type Commit struct{ ID string; Device model.DeviceID; DeviceName string; Clock model.Stamp; Parents []string; Ops []Op }
func SealCommit(c Commit, logKey *secret.Key, random io.Reader) ([]byte, error)   // JSON -> zstd -> Seal(aad="commit/v1"||id)
func OpenCommit(sealed []byte, logKey *secret.Key) (Commit, error)
func SealPack([]Commit, ...) / OpenPack
type LogRemote interface{ EnsureFolder(...); CreateSmallFile(...); ListChildren(...); DownloadAll(...); Delete(...); FindByProperties(...) }
type CommitStore interface{ PutCommit(...); LatestOwnCommit(...); UnreferencedRemoteHeads(...); MarkReferenced(...) }
func New(deps Deps, cfg Config) *Journal                     // Config{Device, DeviceName, PackEvery 500}
func (j *Journal) Record(ctx context.Context, commitID string, ops []Op) (Commit, error) // idempotent by commitID name check on retry
func (j *Journal) Ingest(ctx context.Context, logFiles []drive.File) ([]Commit, error)
func (j *Journal) PackIfDue(ctx context.Context) error       // write pack first, then delete singles; readers dedupe by id
```

Ops carry everything needed to apply them without extra Drive calls (D17).

**syncer, outgoing**

```go
type RemoteStore interface { CreateSmallFile; StartResumableUpload; ResumeUpload; Copy; Update; Delete; Get; FindByProperties; EnsureFolder }
type LocalView interface { ReadyOps; AssignBatch; SnapshotBody; MarkSynced; ObjectFacts; ... } // implemented by workspace + index
type Committer interface{ Record(ctx, commitID string, ops []journal.Op) (journal.Commit, error) }
func NewOutgoing(deps OutgoingDeps, cfg OutgoingConfig) *Outgoing  // Quiet 2s, MaxBatch 200, Uploads 4, Hashing 2
func (o *Outgoing) Run(ctx context.Context) error
func (o *Outgoing) FlushNow(ctx context.Context) error            // test hook and `vault sync`
func ScheduleNetOps(ops []NetOp) [][]NetOp                         // dependency levels; small files first inside a level
```

Pipeline:
1. Collect ready ops.
2. Reduce them to net effect.
3. Persist the batch (net ops plus a pre-chosen commit ID) in `outgoing_batches`.
4. Run the hashing pool: plaintext SHA-256 into a content MAC, then a dedup lookup via `ObjectsByContentMAC` and the trash table, using `Copy` on a hit.
5. Execute by level: folders before children; metadata through the budget; bodies from the snapshot, with MD5 computed while streaming.
6. Verify the MD5 against Drive's `md5Checksum` and re-upload on mismatch.
7. `Record` the commit.
8. Delete the batch's pending ops and set objects clean unless newer ops exist.

**Modify uploads a new Drive file**, with `appProperties {vid, oid, ver=stamp}`. The previous Drive file is deleted permanently after the commit (D7). Every step is idempotent on replay: creates first check `FindByProperties(oid, ver)` when `attempts > 0`; moves set the parents absolutely; deletes accept 404.

**Resumable state:** the session URI and the confirmed offset are stored in the batch's net-op payload, updated by `onConfirmed`.

Commits (about 14): HLC; commit codec; packs; Record and parents; Ingest; scheduler; batch persistence; hashing and dedup; executor (metadata); executor (bodies, resumable, MD5 verify); commit and cleanup; replay on start; FlushNow; status counters for "not backed up".

Tests that gate the merge, on one device against the fake:
- 1,000 files and folders arrive with correct nesting and encrypted names.
- A rename or move of a 1,000-file folder issues zero upload requests (fake request log).
- Editor temp files never reach Drive or the log.
- Crash and replay at every stage, done by a context cancel plus discard and reopen of the same home dir: no duplicates on Drive and exactly one commit.
- A 64 MiB file with `AfterBytes` fault injection resumes and never re-sends confirmed bytes. The 4 GB case is proportional and runs in e2e only.
- Corrupted MD5 (fault rewrites the response) leads to re-upload.
- Under synctest, 5,000 small files with the fake's realistic quota finish with ≤3 responses of 403 or 429.
- Commit parent rules. A pack at 500 commits replaces the singles, and readers see no gap.

### Step 6: `feat/06a-syncer-incoming`, `feat/06b-join-restore`, `feat/06c-verify`

**Incoming**

```go
type ChangeSource interface{ StartPageToken(ctx) (string, error); Changes(ctx, token string) (drive.ChangesPage, error); ListChildren(...) }
type RemoteApplier interface{ ApplyRemoteOp(ctx context.Context, op journal.Op, from model.DeviceID) (ApplyResult, error) } // workspace
func NewIncoming(deps IncomingDeps, cfg IncomingConfig) *Incoming // Active 10s, Idle 30s, ActivityWindow 5m
func (i *Incoming) PollOnce(ctx context.Context) error
func (i *Incoming) Run(ctx context.Context) error
func (i *Incoming) RebuildFromListing(ctx context.Context) error   // parallel list of Vault/, all log files; also used by join
```

Behaviour:
- Changes whose parent is a `log/<device>` folder, or a new device folder, are fed to `journal.Ingest`.
- Unapplied commits are applied in clock order. A commit is held until all its parents are present (D36).
- Each op applies only if `op.Version.After(object.version)`.
- Applying updates the index and tree, calls `InvalidateContent` and `InvalidateEntry`, and schedules a background download for affected Local files. A Partial file has its bitmap cleared instead.
- Raw `tree/` changes that no commit explains produce a warning in the log.

**LWW loser rule (D19):** the device whose version lost moves its own losing body to trash with reason `superseded by <winner>`, and records a `superseded` op.
- If the losing version is local and unsynced, it is uploaded straight into `trash/`.
- If the losing version is already on Drive (we uploaded it before seeing the winner), our Drive file is moved into `trash/`.

**Join and restore**

```go
func (r *Restorer) RestoreAll(ctx context.Context) error
func (r *Restorer) Pause()
func (r *Restorer) Resume()
func (r *Restorer) Progress() RestoreProgress
```

- Uses `ListChildren` per folder in parallel (errgroup) and fetches commit packs and singles in parallel.
- Bodies download with 4 files in parallel, and up to 4 parallel 8 MiB ranges for a single large file.
- Progress lives in the segments bitmap, so a restart never re-downloads finished segments. The pause flag lives in `kv`.
- Restore downloads yield while pending uploads exist. Foreground reads are never blocked (D38).
- The restore ends with a shallow verify.

**Verify**

```go
func (v *Verifier) Verify(ctx context.Context, deep bool) (Report, error)
```

Shallow verify makes one `ListChildren` per folder and compares Drive's MD5 with the index's `body_md5`. Deep verify also streams every body through `vaultfile.VerifyBody` and compares the content MAC.

Commits (about 14): poller cadence; ingest routing; causal ordering; ApplyRemoteOp per kind; LWW and superseded; background replace of Local files; rebuild from listing; join flow; restorer with persistence and pause; parallel range download; verify shallow; verify deep; token-expired fallback; "not backed up" health status.

Tests that gate the merge, two devices (§7):
- An edit on A is visible on B after one A flush and one B poll. B's history shows the commit in A's lane.
- Concurrent edits: newer stamp wins on both devices; the loser is in trash as superseded with the right device name. Run both orderings and both "seen or unseen before upload" variants.
- Rename on A while B modifies.
- Folder created on A, file moved into it on B: causal hold works.
- Join on demand with 10,000 objects: the tree is complete, the fake shows zero body downloads, and the elapsed time is asserted generously.
- Restore everything, then kill (discard) mid-restore and resume: no segment is downloaded twice (fake byte counters).
- A corrupted body is reported by deep verify, and only that file.
- An expired page token triggers rebuild with no duplicates.
- Network drop (fault: all requests fail for N seconds) mid-sync: completes with no duplicates or lost files.

### Step 7: `feat/07-trash-moveout-dedup-procfilter` (packages `trash`, `procfilter`; workspace and syncer hooks)

**trash**

```go
type Entry struct{ ID string; ObjectID model.ObjectID; OriginalPath string; DeletedAt time.Time; Device model.DeviceID; DeviceName string; Reason Reason; Size int64; FileCount int; Folder bool }
type Remote interface{ Update(...); Delete(...); EnsureFolder(...) }
type Recorder interface{ RecordLocal(ctx context.Context, ops []journal.Op) error } // goes through outgoing as a batch
func NewManager(deps Deps, cfg Config) *Manager                                    // Retention 30d, now func
func (m *Manager) List(ctx context.Context) ([]Entry, error)
func (m *Manager) Restore(ctx context.Context, id string) (restoredPath string, err error) // "name (restored).ext", recreates parents
func (m *Manager) Purge(ctx context.Context, id string) error
func (m *Manager) Empty(ctx context.Context) (count int, bytes int64, err error)
func (m *Manager) ExpireOld(ctx context.Context) (int, error)                       // daily + at unlock; logs trash_purge reason=expired (D33)
type MoveOutDetector struct{ ... } // NewMoveOutDetector(window 10s, now)
func (d *MoveOutDetector) RecordRead(id model.ObjectID, pid uint32, off, n, size int64) // interval coverage per (oid,pid), bounded LRU
func (d *MoveOutDetector) ReadWholeRecently(id model.ObjectID, pid uint32) bool
type Notifier interface{ Notify(ctx context.Context, summary, body string) error }
```

- **Trashed folders:** subtree rows stay in `objects` with `trash_id` set, hidden from the tree, on every device (D32).
- **Coalescing:** handled by the reducer (child deletes subsumed by an ancestor delete) together with the 3 s delete hold. A folder counts as moved out if every file delete in it was a whole-read by the deleting PID (D34).
- **Dedup:** the hashing step, now also matching trash items, uses `Copy`. `cp` inside the vault is therefore free.

**procfilter**

```go
func New(denied []string, processName func(pid uint32) (string, error)) *Filter // default reads /proc/<pid>/comm
func (f *Filter) Denies(pid uint32) bool
```

Workspace `Open` and `ReadAt` return `EIO` for denied callers on Cloud-only and Partial objects.

Commits (about 10): entries and store; restore; purge and empty; expiry; move-out detector; wiring into workspace unlink; folder coalescing and notification text; dedup via trash; procfilter; workspace deny hook.

Tests that gate the merge:
- Unit tests for each manager method.
- Two-device: a restore on A is seen on B. Expiry on both devices races, a 404 counts as success, and there is a single purge per device.
- FUSE: `mv` to another filesystem is labelled moved out and the fake notifier is called once. `mv` of a 400-file folder out gives one trash entry, one Drive call and one notification. Copying the file back in gives a create with a `Copy` request, no upload, and the trash entry untouched. `cat` is denied on a Cloud-only file when the deny list is `["cat"]` (real procfilter).

### Step 8: `feat/08-api-cli-notify-systemd` (packages `api`, `notify`, `config`, `daemon`, `cmd/vaultd`, `cmd/vault`, `packaging/systemd`)

**config**: `Load(path) (Config, error)` with validation and defaults; `Layout`, resolved from `--home` or XDG (D23).

**notify**: `New(conn) (*Notifier, error)` wrapping esiqveland/notify. Its test starts a private `dbus-daemon --session --print-address` and skips if that's unavailable.

**api**: a server over `$XDG_RUNTIME_DIR/vault/daemon.sock` (or `<home>/daemon.sock`).
- The socket is mode 0600. Peer UID is checked with `unix.GetsockoptUcred` in a listener wrapper.
- Endpoints: the spec's list, plus `POST /signin`, `POST /create`, `POST /join`, `POST /verify`, `POST /devices/retire`, `GET/PUT /settings`, `POST /sync` (D26).
- `GET /events` uses go-sse.
- `api` declares `type Service interface{...}`, which the daemon implements. `api.Client` is shared by the CLI and the panel.

**daemon**: a state machine of Locked, Unlocking, Mounted and Locking.

```go
func New(layout config.Layout, cfg config.Config, deps Deps) *Daemon
func (d *Daemon) Unlock(ctx context.Context, password []byte) error   // local vault.header -> keys -> token -> catch up -> mount
func (d *Daemon) Lock(ctx context.Context) error                     // cancel ctx, unmount, Destroy keys
func (d *Daemon) BeginSignIn(ctx) (authURL string, err error)
func (d *Daemon) CreateVault(ctx, password []byte, deviceName string) error
func (d *Daemon) JoinVault(ctx, password []byte, deviceName string, mode JoinMode) error
```

`vault.header` is cached locally, so unlock can derive the token key before touching Drive (D11).

**cmd/vaultd**: flags `--home`, `--log-level`, and a hidden `--drive-endpoint` (D24). It runs `secret.HardenProcess` first, handles SIGTERM and SIGINT by locking, and logs to `daemon.log`.

**cmd/vault**: cobra commands `status`, `lock`, `unlock`, `create`, `join [--on-demand]`, `signin`, `download <path>`, `free <path>`, `verify [--deep]`, `trash ls|restore|purge|empty`, `sync`. Passwords are read with x/term (D25).

**packaging/systemd/vaultd.service**: `Type=simple`, `LimitMEMLOCK=infinity` (best effort), `LimitCORE=0`, `Restart=on-failure`.

Commits (about 12): config; layout; notify; API server and peercred; SSE events; API client; daemon state machine; onboarding flows; vaultd main; CLI; systemd unit; e2e harness.

Tests that gate the merge:
- API: peercred rejects (simulated via an injected UID check), JSON round trips, SSE delivers status changes.
- Daemon: unlock, mount, lock and unmount with keys destroyed; a wrong password; a newer format refuses to mount.
- e2e (`-tags e2e`): `tools/drivefake` plus two `vaultd --home` processes with real mounts:
  - create on A, join on B, edit on A visible on B
  - `kill -9` A within 5 s of a save, then B restore-all has everything except at most that save
  - `kill -9` B mid-restore, then resume
  - plaintext grep over both homes

### Step 9: `feat/09-panel` (`panel/`, `panel/layout`, `packaging/nautilus`)

- `panel/main.go` binds a Go `App` that wraps `api.Client` and forwards SSE events to `runtime.EventsEmit`.
- The frontend is plain TypeScript with Vite and SVG: a locked screen, Tree, History (200 commits per page, path filter), Trash (with confirmations), and Settings.
- A hand-written `bridge.ts` over `window.go.main.App` replaces the Wails-generated bindings.
- `panel/layout.AssignLanes(commits) []Row` is pure Go and computes lanes, edges and merge crossings. Retired devices are grey. It is unit-tested.
- Nautilus scripts call `vault download` and `vault free` with `$NAUTILUS_SCRIPT_SELECTED_FILE_PATHS`.

Commits (about 10): layout package; App bindings; frontend skeleton and locked screen; tree view; history SVG; trash view; settings; build target; Nautilus scripts and installer.

Tests that gate the merge: `panel/layout` unit tests; `npm run build` and `tsc --noEmit` pass; the tagged `go build` succeeds; a manual smoke test launches against `tools/drivefake` (DISPLAY is available).

### Step 10: acceptance

A checklist doc maps every acceptance box to an automated test, an e2e test, or a manual step; see §8.

---

## 6. FUSE integration tests

- **Location:** `internal/fusefs/fusefs_integration_test.go`, plus scenario tests under `internal/workspace`. All use the helper `internal/fusefs/fusetest.Mount(t, store) (mountPoint string)`.
- **Skip rules:** the helper skips (`t.Skip`) when any of these hold:
  1. `VAULT_SKIP_FUSE=1` is set.
  2. `/dev/fuse` is missing or can't be opened read-write.
  3. Neither `fusermount3` nor `fusermount` is on PATH.
  4. `fusefs.Mount` returns `EPERM` or `EACCES`, which happens in containers.
- **Mount lifecycle:** each test gets `t.TempDir()` as the mount point and a fresh workspace on its own temp home. `t.Cleanup` calls `Unmount`, retrying for up to 2 s, and falls back to `fusermount3 -u -z`. The serving goroutines are owned by the `Mount` and stopped by Unmount.
- **External processes:** real programs (`cp`, `mv`, `sh -c`, `cat`, `dd`) run through `exec.CommandContext` with a timeout. This exercises the exact syscalls those programs make, keeps PID semantics real for procfilter and move-out tests, and avoids any self-deadlock.
- **Running FUSE tests under `-race`:** these tests still run under `-race`. They don't use synctest; timing uses short configured durations (quiet period 50 ms). They wait on conditions such as "pending ops reached state X", not on sleeps.
- **Scenarios:**
  - POSIX basics: create, read, write, seek, append, truncate up and down, mkdir, ENOTEMPTY rmdir, rename of files and folders, rename over, readdir, chmod +x, utimens. Symlink and link give EPERM, mknod gives EPERM, setxattr gives ENOTSUP. fsync reaches the cache file.
  - Writes at segment edges compared with a reference file.
  - Editor patterns:
    - gedit/GIO: write `.goutputstream-XXXX`, fsync, rename over.
    - vim: rename to `file~`, write a new file, unlink `file~`; `.swp` create and delete.
    - LibreOffice: `.~lock.x#` plus a temp file and rename.
    - Each asserts that the reducer yields exactly one `modify` of the original oid.
  - `cp -r` of a tree, and `cp` inside the vault (dedup in step 7).
  - Cross-filesystem `mv` out to a `t.TempDir()` outside the mount, of a file and of a 400-file folder (step 7).
  - Nautilus pattern: `mkdir -p .Trash-1000/files` and rename into it.
  - Deny list via `cat`.
  - Plaintext grep of the home dir.
- **Real-time numbers:** a separate `-tags fuse_long` file runs the 2 s quiet period and the 10 s and 3 s windows for real.

## 7. Multi-device tests

`internal/devicetest` (test-only package):

```go
type Network struct{ Drive *drivefake.Server }          // one fake Drive shared by all devices
func NewNetwork(t *testing.T, opts ...drivefake.Option) *Network
type Device struct{ Name string; ID model.DeviceID; Home string; Workspace *workspace.Workspace; Out *syncer.Outgoing; In *syncer.Incoming; Trash *trash.Manager; Journal *journal.Journal; Index *index.Store; Notifier *FakeNotifier }
func (n *Network) CreateDevice(t, name string, wall func() time.Time) *Device      // first device: CreateVault
func (n *Network) JoinDevice(t, name string, mode JoinMode, wall func() time.Time) *Device
func (d *Device) WriteFile(t, path string, data []byte)    // through workspace.Store, as FUSE would
func (d *Device) ReadFile(t, path string) []byte
func (d *Device) Rename / Remove / Mkdir
func (d *Device) Sync(t)          // Out.FlushNow
func (d *Device) Poll(t)          // In.PollOnce
func (d *Device) Crash(t)         // cancel ctx, drop all in-memory state, no graceful close
func (d *Device) Restart(t) *Device // reopen same Home and DeviceID, replay pending ops
func (d *Device) Wipe(t)          // delete Home: brand-new laptop
```

- Each device has its own temp home, SQLite, cache, injected wall clock (for clock-skew cases) and device ID. All of them share one `drivefake.Server` through its in-process HTTP client.
- Tests are deterministic. There are no background goroutines unless a test opts in with `d.Start(ctx)`. `Sync` and `Poll` are explicit steps. The cadence tests that use background loops run inside `synctest.Test`.
- **Crash at a precise point:** a fault such as `Fault{Match: uploadOf(oid), AfterBytes: n}` combined with a hook that cancels the context.
- **Matrix:** ordering; LWW both ways; superseded-to-trash; the trash-restore race; crash and replay at each pipeline stage; join on demand vs restore everything; wipe and rejoin; verify after corruption; network drop.
- The FUSE-level two-device check (two mounts, A and B) lives only in e2e (§5 Step 8).

## 8. Delivery: what I expect vs what gets deferred

Realistically delivered and tested here:
- Steps 1–8 fully, against the fake Drive. That is all of crypto, sync, journal, trash, FUSE, the CLI and the daemon.
- The panel built with `go build` and webkit2_41, with manual smoke tests.
- An e2e suite covering the recovery drill with `tools/drivefake` and two `--home` instances.

Acceptance items, sorted by how they can be checked:

- **Automated with the fake or real FUSE:**
  - create, unlock, mount; lock, unmount
  - plaintext grep
  - resumable upload and resume after kill (64 MiB in CI; 4 GB manual or e2e with the fake on disk)
  - join on demand with 10k files
  - Nautilus-style thumbnailer denial (via the deny list)
  - rename and move of 1,000 files is metadata only
  - moved-out file and folder; drag back in deduplicates
  - trash restore, purge, expiry
  - superseded
  - Free up space
  - 5,000 files with ≤3 rate-limit errors (against the fake's quota model, not real Google)
  - network drop
  - every recovery-drill item except the VM power cut
- **Only with real Google credentials, deferred to the owner:**
  - Drive web UI shows only ciphertext
  - real OAuth sign-in and "In production" consent
  - real rate-limit behaviour
  - VLC plays a 4 GB file within 3 s from real Drive
  - the real 750 GB/day cap
  - the acceptance run on two machines (step 10)
- **Hard to prove:** `strings` on a core dump and on swap. The dump is prevented by `PR_SET_DUMPABLE=0` and `RLIMIT_CORE=0`, both tested. For swap, a manual check: run `swapoff` and `swapon` under memory pressure, then grep. We assert `VmLck > 0`.
- **Panel:** built without the Wails CLI. If `go build` with tags turns out not to inject the runtime correctly, fall back to `go run github.com/wailsapp/wails/v2/cmd/wails@v2.17.0 build`, which installs nothing globally. This needs the user's OK.

Risks:
1. **Scope:** realistically 15–25k lines of Go. Steps 5 and 6 are the riskiest, and sync correctness dominates.
2. **Fake fidelity:** a REST fake can drift from real Drive: the `q` grammar, the 308 semantics, error reasons, token-expiry status codes, `md5Checksum` timing, appProperties limits (124 bytes per key+value). Mitigations: strict 400 for anything unsupported, and a documented `go test -tags realdrive` smoke suite to run once credentials exist.
3. **go-fuse quirks:** in-process serving under `-race`, notify calls racing with lookups, `KEEP_CACHE` invalidation. Mitigation: use external processes and call invalidation outside workspace locks.
4. **Key hygiene:** keys can leak via heap copies (AEAD key schedules, Go stack copying). `mlockall` mitigates this and may fail under a low `RLIMIT_MEMLOCK` (systemd user-manager defaults). Documented as a residual risk.
5. **Snapshot cost:** ext4 has no reflink, so a 4 GB file being edited means a 4 GB copy before upload. Acceptable for v1, but disk-heavy.
6. **Drive behaviour:** whether files.copy keeps `md5Checksum` identical (it should, being a byte copy), and Drive's "3 creates/s" figure (the spec itself is unsure).
7. **Freshness:** Wails v2.17.0 and go-fuse v2.12.0 are days old. Pin to these and fall back one minor version if a regression appears.

## 9. Open decisions (ambiguous in the spec): proposal and reason

| # | Topic | Proposal | Reason |
|---|---|---|---|
| D1 | Module root | Repo root | `.gitignore` already uses root-relative `panel/...` paths; the `vault/` in §14 is the repo name |
| D2 | Module path | `github.com/cosmoboy/vault` | From the spec author's handle; trivial to change |
| D3 | Go version | `go 1.26.0`, `toolchain go1.26.8` | Current deps require 1.26; the toolchain is cached; 1.22 is EOL; synctest becomes available |
| D4 | Extra `internal/model` | Shared value types only | Avoids import cycles and adapter boilerplate across six packages |
| D5 | Extra `internal/workspace` | The fusefs store implementation plus remote-apply surface | FUSE and the syncer both mutate tree, index and cache atomically. One owner of those invariants beats duplicating them in fusefs and syncer. Step 4's "purely local store" is this package |
| D6 | Fake Drive level | HTTP-level fake (`http.Handler`) with an in-process RoundTripper; the real `drive.Client` runs on top in every test | One fake instead of two drifting ones. It exercises request encoding, retries, the budget and the resumable protocol. "Same interface" is read as the Drive REST interface. The syncer still depends only on its own `RemoteStore` interface |
| D7 | Modify on Drive | Upload a **new** Drive file per content version (`ver` appProperty), delete the old one after commit | The log never points at overwritten data. Concurrent edits can't silently overwrite each other (Drive v3 has no If-Match). The loser's body survives for trash. Cost: one extra metadata call per modify |
| D8 | VLT1 file ID | A random BodyID per body, not the oid. Key and ID rotate on create and on truncate-to-zero; in-place edits keep the key with fresh nonces | Rename-over and dedup copies move bodies between oids. Rotating per edit would re-encrypt GBs. Residual risk: Drive could roll back a single segment to an older version of the same body |
| D9 | Empty and aligned files | At least one segment; the last is always flagged final | Makes truncation to zero, or to a segment boundary, detectable |
| D10 | Header size | Fixed 128 bytes (94 used) | Constant offsets; room for future fields |
| D11 | vault.header | JSON; cached locally at `<home>/vault.header` | Unlock must derive the token key before it can reach Drive; the header holds no secrets |
| D12 | HKDF | salt = vaultID; info `vault/v1/<purpose>`; index split into seal and mac | Domain separation; no single key used for both AEAD and HMAC |
| D13 | "Encrypted content hash" | HMAC-SHA256(index/mac, SHA-256(plaintext)) | Dedup needs an indexed equality lookup that works across devices; randomised encryption can't do that |
| D14 | Name associated data | vaultID only | Spec: a move only changes the parent, so the name ciphertext must not depend on it |
| D15 | Queue timing | Metadata ops append at once with `not_before`; modify appends at close; Dirty at first write; deletes held 3 s; extra `outgoing_batches` table | Durability first (a crash inside the quiet window still uploads), with no SQLite write per `write()` syscall. Coalescing falls out of the reducer |
| D16 | Rename over existing | Target keeps its oid; `modify{replacedFrom}`; source Drive object deleted permanently after commit (no trash) | Spec says "modify of the target"; the source's content lives on in the target |
| D17 | Commit op fields | Ops carry parent oid, name, Drive ID, Drive name, size, mtime, MAC, MD5, version | Remote apply needs no extra Drive calls, which matters for the budget and for the 10k-file join time |
| D18 | Local commits and secrets on disk | The `commits` table stores the sealed blob; names and paths in pending ops and trash are sealed | Acceptance: no plaintext under the home dir |
| D19 | LWW loser handling | The losing device trashes its own body (`superseded by <winner>`) | Symmetric and race-free; covers both "unsynced" and "already uploaded" losers |
| D20 | Upload snapshot | FICLONE reflink, falling back to a full copy | The spec requires a snapshot; ext4 here has no reflink |
| D21 | statfs | Report the cache filesystem's free and total space | Files land locally first; prevents Nautilus refusing copies or lying about space |
| D22 | utimens | Supported; synced as metadata (Drive `modifiedTime`) | `cp -p`, rsync and tar need it; the spec only lists chmod |
| D23 | Paths | Without `--home`: XDG split per §6. With `--home`: everything under it (`config.toml`, `index.db`, `cache/`, `token.enc`, `daemon.sock`, `device-id`, `daemon.log`, `vault.header`). Error if the socket path exceeds 107 bytes | Matches both §6 and §14 |
| D24 | Dev and test Drive | Hidden `vaultd --drive-endpoint URL` (skips OAuth) plus `tools/drivefake` | The only way to run the daemon, panel and e2e without Google credentials |
| D25 | CLI scope | Add `unlock`, `create`, `join`, `signin`, `verify`, `trash`, `sync` | Headless onboarding and the recovery drill; the spec already uses `vault verify` |
| D26 | API scope | Add `/signin`, `/create`, `/join`, `/verify`, `/devices/retire`, `/settings`, `/sync` | The panel and CLI need them for §11–13 |
| D27 | OAuth browser | The daemon returns the URL; the client (panel or CLI) opens it | A systemd service may lack the display environment |
| D28 | OAuth client file | Embedded placeholder JSON; config `oauth_client_file` overrides; clear error when the placeholder is still in place | The spec wants it compiled in, but no credentials exist yet |
| D29 | Key memory | memguard for keys plus best-effort `mlockall`; status flag on failure | Covers Argon2 and AEAD heap copies |
| D30 | Panel | Wails v2 via the tagged `go build`; hand-written TS bridge; lane layout in pure Go (`panel/layout`) | No Wails CLI; layout is tested with go test |
| D31 | Device ID and retire | `d-` + 16 hex, stored in home; retire sets `retired` in the encrypted `.vdev` record | Matches the spec's example; survives restarts |
| D32 | Trashed subtree | Rows stay in `objects` with `trash_id` on every device | Restore needs the subtree's metadata without listing Drive |
| D33 | Expiry logging | Expiry logs `trash_purge` (reason expired); duplicates across devices are harmless | Every change to vault contents is logged |
| D34 | Folder move-out | Moved out only if every file in it was read whole by the deleting PID | Conservative; otherwise labelled "deleted" |
| D35 | Page token expiry | Treat 404, or 400 with reason `invalid`, as expired and rebuild | Real Drive behaviour is unverified |
| D36 | Apply order | Clock order, holding commits whose parents are missing | Causality across devices (move into a folder created elsewhere) |
| D37 | Inode numbers | Per-session counter mapped to the oid | No hash collisions; the kernel only needs stability within a mount |
| D38 | Upload vs download priority | Background restore and refresh downloads pause while uploads are pending; foreground reads never wait | Durability first, without freezing apps |
| D39 | Password transport | Socket JSON, buffers wiped after use; CLI reads with x/term | Spec §4 |
| D40 | SQLite durability | WAL with `synchronous=NORMAL` | Survives process crashes; a power cut may lose the last transaction, consistent with "a few seconds" |
| D41 | Retry ceiling | 8 attempts per request, then requeue via `next_attempt` | Truncated backoff without wedging a worker |
| D42 | Packs | `pack-<first-clock>-<last-clock>.vpack`; write pack, then delete singles; readers dedupe by commit ID | Crash-safe compaction |
| D43 | `RENAME_EXCHANGE` | `EINVAL` | Rare, and complex to model in the log |
| D44 | Branching | One branch per step, split into a/b/c for steps 2, 5, 6; `--no-ff` merges; every commit passes `make check` | User's process plus spec rule 8 |

### Critical Files for Implementation
- /home/revant-sinha/Desktop/vdeskproject/Vault Encrypted Drive-Backed Storage, v1 Spec.md
- /home/revant-sinha/Desktop/vdeskproject/.gitignore (needs the `dist/.gitkeep` exception)
- /home/revant-sinha/Desktop/vdeskproject/logs/README.md (where this plan should be saved: `logs/decisions/01-plan.md`)
- To create: `go.mod`, `internal/vaultfile/body.go`, `internal/drive/drivefake/server.go`, `internal/workspace/workspace.go`, `internal/syncer/reduce.go`, `internal/devicetest/device.go`
