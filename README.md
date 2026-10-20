# Vault

Encrypted, Drive-backed storage that mounts as a normal folder. See `Vault Encrypted Drive-Backed Storage, v1 Spec.md`.

## Development

Requires Go with `GOTOOLCHAIN=auto` (the module pins go1.26), and `/dev/fuse` for the mount tests.

```
make check   # gofmt, go vet, staticcheck, go test -race
make build   # vaultd and vault into bin/
make panel   # control panel (needs libgtk-3-dev, libwebkit2gtk-4.1-dev)
```

Design decisions live in `logs/decisions/`.
