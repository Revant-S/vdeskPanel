# Daemon API routes

JSON over HTTP on a Unix socket (`<home>/daemon.sock` with `--home`, else `$XDG_RUNTIME_DIR/vault/daemon.sock`).
Errors return a non-2xx status and an `ErrorBody`. Password bodies are raw `application/octet-stream`, at most 1 KiB.

| Method | Path | Request | Response |
|---|---|---|---|
| GET | /status | | Status |
| POST | /signin | | `{"authURL": "..."}` |
| POST | /signout | | |
| POST | /create?deviceName= | password bytes | |
| POST | /join?deviceName=&mode=restore\|on-demand | password bytes | |
| POST | /unlock | password bytes | |
| POST | /lock | | |
| GET | /tree?path= | | []TreeEntry (children of path) |
| GET | /commits?before=&limit=&path= | | CommitPage |
| GET | /devices | | []DeviceView |
| POST | /devices/retire | `{"id": "..."}` | |
| GET | /trash | | []TrashItem |
| POST | /trash/restore | `{"id": "..."}` | `{"path": "..."}` |
| POST | /trash/purge | `{"id": "..."}` | |
| POST | /trash/empty | | EmptyTrashResult |
| POST | /download | `{"path": "..."}` | |
| POST | /free | `{"path": "..."}` | |
| POST | /sync | | |
| POST | /verify?deep=true\|false | | VerifyReport |
| POST | /restore/pause?paused=true\|false | | |
| GET | /settings | | Settings |
| PUT | /settings | Settings | |
| GET | /events | | text/event-stream of Event (`data: <json>`) |

Error codes map to the sentinels in service.go: wrong-password, locked, not-found, busy, newer-format, invalid-request.
