package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/cosmoboy/vault/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var nowFunc = time.Now

func formatStatus(status api.Status, now time.Time) string {
	var out strings.Builder
	fmt.Fprintf(&out, "State: %s\n", status.State)
	if status.State == api.StateMounted || status.State == api.StateRestoring {
		fmt.Fprintf(&out, "Mounted at %s\n", status.MountPoint)
		fmt.Fprintln(&out, backupLine(status, now))
	}
	if r := status.Restore; r != nil {
		fmt.Fprintf(&out, "Restoring: %d of %d files, %s of %s%s\n", r.FilesDone, r.FilesTotal, humanBytes(r.BytesDone),
			humanBytes(r.BytesTotal), map[bool]string{true: " (paused)"}[r.Paused])
	}
	if status.CorruptFiles > 0 {
		fmt.Fprintf(&out, "%d files failed an integrity check and are not uploaded\n", status.CorruptFiles)
	}
	if status.KeysMaySwap {
		fmt.Fprintln(&out, "Note: memory locking is limited; keys could reach swap")
	}
	return out.String()
}

func backupLine(status api.Status, now time.Time) string {
	if status.NotBackedUpFiles > 0 {
		return fmt.Sprintf("%d files not backed up yet · %s", status.NotBackedUpFiles, humanBytes(status.NotBackedUpBytes))
	}
	if status.LastBackedUp.IsZero() {
		return "All backed up"
	}
	return "All backed up · " + ago(now.Sub(status.LastBackedUp))
}

func ago(elapsed time.Duration) string {
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%d min ago", int(elapsed.Minutes()))
	case elapsed < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(elapsed.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(elapsed.Hours()/24))
}

func humanBytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value, unit := float64(n), 0
	for value >= 1000 && unit < len(units)-1 {
		value, unit = value/1000, unit+1
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.0f %s", value, units[unit])
}

// readPassword reads without echo. The caller sends the bytes, and the client wipes them.
func readPassword(cmd *cobra.Command, prompt string) ([]byte, error) {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(cmd.OutOrStdout())
	return password, err
}

func readNewPassword(cmd *cobra.Command) ([]byte, error) {
	first, err := readPassword(cmd, "New vault password (at least 12 characters): ")
	if err != nil {
		return nil, err
	}
	second, err := readPassword(cmd, "Repeat the password: ")
	defer secret.Wipe(second)
	if err != nil {
		secret.Wipe(first)
		return nil, err
	}
	if string(first) != string(second) {
		secret.Wipe(first)
		return nil, fmt.Errorf("the passwords differ")
	}
	return first, nil
}
