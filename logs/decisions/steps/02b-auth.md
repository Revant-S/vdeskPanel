# Step 2b: auth

Decisions and deviations made while building `internal/auth` on `feat/02b-auth`.

## Dependencies

- **golang.org/x/oauth2 v0.37.0** is the only new module (it's in the plan's table).
- **No `oauth2/google`.** `NewConfig` uses `golang.org/x/oauth2/endpoints.Google`, which is the same value `google.Endpoint` points at. `oauth2/google` would pull in `cloud.google.com/go/compute/metadata` for service-account and GCE support we never use. `drive/v3` may bring that module in later through `google.golang.org/api/transport`; auth still doesn't need it.
- **No `google.ConfigFromJSON`.** For the same reason, the "installed" client JSON is parsed with a small struct. Only `client_id` and `client_secret` are read. The endpoints are fixed to Google's, so they aren't taken from the file.
- **No `pkg/browser`** (R25). The package returns the URL and never opens a browser (D27).

## Credentials

- The placeholder `oauth_client.json` uses `replace-me` values. `LoadClientCredentials` returns `ErrPlaceholderCredentials` when the client ID starts with `replace-me` or the secret is `replace-me`, whichever file supplied them.
- If the override path is empty or missing, the embedded file is used. Any other read error, such as a permission problem, is returned and doesn't fall back. A file without an `"installed"` section (for example a "web" client) fails with `ErrInvalidCredentials`.
- `EmbeddedClientFile()` returns a copy of the `go:embed` bytes, so the embedded data can't be changed at runtime.

## Flow

- `Begin(ctx)` binds the listener, builds the auth URL and returns `wait`. The listener is shut down when `wait` returns or when Begin's `ctx` ends, whichever happens first. Callers should give Begin a ctx tied to the sign-in request so an abandoned sign-in doesn't hold a port open.
- `wait` can be called once (`ErrAlreadyWaiting` after that). Its ctx bounds the wait for the callback and the code exchange. On timeout or cancel the listener is closed right away.
- **State check fails closed.** The first request that carries `state`, `code` or `error` claims the sign-in. A wrong state ends the flow with `ErrStateMismatch` and nothing is exchanged. Later callbacks get 409. Requests with none of those parameters get 400 and don't claim the flow, and paths other than `/` get 404 (favicon). The state is compared in constant time and comes from `crypto/rand.Text()` (26 base32 characters, 130 bits).
- **The callback page reports the real outcome.** The handler waits for the code exchange to finish before answering. The browser sees "Signed in, you can close this tab." only if the exchange worked; otherwise it sees "Sign-in failed. Return to Vault and try again." The page is plain text and never echoes the error.
- **Extra checks beyond the brief.** The flow fails with `ErrNoRefreshToken` if Google returns no refresh token. It fails with `ErrScopeNotGranted` if the token's `scope` field leaves out `drive.file`, since Google's granular consent lets a user untick it. It refuses a listener not on a loopback address (`ErrNotLoopback`).
- The redirect URI is `http://127.0.0.1:<port>/`. Google allows any port for desktop loopback redirects.

## Token store

- **Signature kept as briefed.** It's `NewTokenStore(path, keyring)`, not the plan's `*secret.Key`, because the arbiter's R6 Keyring replaced bare keys. Nonces come from `crypto/rand.Reader` directly, not an injected `io.Reader`. Tests have no need for deterministic nonces, so the brief's signature was kept.
- `Save` wipes the token JSON buffer after sealing, and `Load` wipes it after decoding. As with crypto's residual heap copies, the `oauth2.Token` strings themselves can't be wiped.
- Writes go to a temp file in the same directory (`os.CreateTemp`, mode 0600), then fsync, rename and a directory fsync. The directory is created with 0700. An existing directory's mode isn't changed.
- A wrong keyring returns `open token: crypto: message failed authentication` (it wraps `crypto.ErrDecrypt`). A destroyed keyring returns a wrapped `crypto.ErrLocked`.

## PersistingSource

- The first call compares the base token with the one on disk. After that it compares with the last token it saved. It saves when the access token changes or a non-empty refresh token changes. If the new token has no refresh token, the saved refresh token is carried over, because Google's refresh response leaves it out.
- **A failed save returns an error from `Token()`** instead of being dropped, and the next call tries again. There's no logger in the signature, and keeping a rotated refresh token only in memory would lose it on restart. If this turns out too strict (a full disk would block Drive calls), the daemon can wrap it.
- `base.Token()` runs outside the mutex, so a concurrency-safe base (such as `config.TokenSource`, a ReuseTokenSource) refreshes on its own schedule. Only the compare-and-save step is serialised.
