package api

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var ErrAlreadyRunning = errors.New("api: another daemon is already listening on the socket")

const staleProbeTimeout = time.Second

// peerUIDReader returns the UID of the process on the other end of conn.
type peerUIDReader func(conn *net.UnixConn) (uint32, error)

// Listen binds the daemon socket following R21: a 0700 directory owned by us, a stale
// socket removed only when nothing answers on it, mode 0600 after bind, and every
// connection from another UID dropped. No umask is set, since that is process-wide.
func Listen(socketPath string) (net.Listener, error) {
	return listenWithPeerCheck(socketPath, readPeerUID, uint32(os.Getuid()))
}

func listenWithPeerCheck(socketPath string, peerUID peerUIDReader, allowedUID uint32) (net.Listener, error) {
	if err := prepareSocketDir(filepath.Dir(socketPath)); err != nil {
		return nil, err
	}
	if err := removeStaleSocket(socketPath); err != nil {
		return nil, err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("restrict socket mode: %w", err)
	}
	return &peerCheckedListener{UnixListener: listener, peerUID: peerUID, allowedUID: allowedUID}, nil
}

func prepareSocketDir(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create socket directory: %w", err)
		}
		// MkdirAll applies the umask; an unusual umask could leave the directory wider.
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("restrict socket directory: %w", err)
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect socket directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("socket directory %s is not a directory", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("socket directory %s is not owned by the current user", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("socket directory %s has mode %o, want 700", dir, info.Mode().Perm())
	}
	return nil
}

func removeStaleSocket(socketPath string) error {
	info, err := os.Lstat(socketPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect socket: %w", err)
	}
	if info.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("%s exists and is not a socket", socketPath)
	}
	conn, err := net.DialTimeout("unix", socketPath, staleProbeTimeout)
	if err == nil {
		_ = conn.Close()
		return ErrAlreadyRunning
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

type peerCheckedListener struct {
	*net.UnixListener
	peerUID    peerUIDReader
	allowedUID uint32
}

// Accept drops connections from other users silently and keeps waiting, so a stranger
// cannot make the HTTP server see an accept error.
func (l *peerCheckedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			return nil, err
		}
		uid, err := l.peerUID(conn)
		if err == nil && uid == l.allowedUID {
			return conn, nil
		}
		_ = conn.Close()
	}
}

func readPeerUID(conn *net.UnixConn) (uint32, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("raw socket: %w", err)
	}
	var credentials *unix.Ucred
	var credentialsErr error
	err = raw.Control(func(fd uintptr) {
		credentials, credentialsErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return 0, fmt.Errorf("control socket: %w", err)
	}
	if credentialsErr != nil {
		return 0, fmt.Errorf("read peer credentials: %w", credentialsErr)
	}
	return credentials.Uid, nil
}
