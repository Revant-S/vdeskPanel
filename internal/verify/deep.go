package verify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"vault/internal/crypto"
	"vault/internal/slots"
	"vault/internal/vaultfile"
)

func (v *Verifier) checkBodies(ctx context.Context, checks []*bodyCheck, report *Report) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	limiter := slots.New(v.config.Downloads)
	var mu sync.Mutex
	var firstErr error
	for _, check := range checks {
		release, err := limiter.Acquire(ctx, check.size)
		if err != nil {
			break
		}
		go func() {
			defer release()
			kind, downloaded, err := v.checkBody(ctx, check)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				return
			}
			report.BodiesVerified++
			report.BytesDownloaded += downloaded
			if kind != "" {
				report.Problems = append(report.Problems, check.problem(kind))
			}
		}()
	}
	limiter.Wait()
	return firstErr
}

// checkBody downloads one ciphertext into a temporary file, which only ever holds data that
// is already encrypted, and authenticates it there.
func (v *Verifier) checkBody(ctx context.Context, check *bodyCheck) (ProblemKind, int64, error) {
	file, err := os.CreateTemp(v.config.TempDir, "verify-*")
	if err != nil {
		return "", 0, fmt.Errorf("verify: create temp file: %w", err)
	}
	defer os.Remove(file.Name())
	defer file.Close()
	err = v.deps.Remote.DownloadAll(ctx, check.driveID, file)
	if errors.Is(err, ErrRemoteNotFound) {
		return MissingOnDrive, 0, nil
	}
	if err != nil {
		return "", 0, wrapRemote("download body of "+string(check.id), err)
	}
	info, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("verify: stat temp file: %w", err)
	}
	kind, err := v.authenticate(file, info.Size(), check)
	return kind, info.Size(), err
}

// authenticate fails only when the keyring is locked; anything wrong with the body itself is
// returned as a problem kind.
func (v *Verifier) authenticate(ciphertext io.ReaderAt, size int64, check *bodyCheck) (ProblemKind, error) {
	var kind ProblemKind
	err := v.deps.Keyring.Use(func(keys *crypto.Keys) error {
		header, plaintextSHA256, err := vaultfile.VerifyBody(ciphertext, size, keys.Content)
		if err != nil {
			v.deps.Logger.Warn("body failed authentication", "oid", check.id, "err", err)
			kind = FailedAuthentication
			return nil
		}
		if !bytes.Equal(header.BodyID[:], check.bodyID) || !hmac.Equal(crypto.ContentMAC(keys, plaintextSHA256), check.contentMAC) {
			v.deps.Logger.Warn("body does not match the index", "oid", check.id)
			kind = IdentityMismatch
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("verify: authenticate body of %s: %w", check.id, err)
	}
	return kind, nil
}
