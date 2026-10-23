package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"vault/internal/api"
	"vault/internal/secret"
)

func formatStatus(status api.Status, now time.Time) string {
	var out strings.Builder
	fmt.Fprintf(&out, "State: %s\n", status.State)
	if status.State == api.StateMounted || status.State == api.StateRestoring {
		fmt.Fprintf(&out, "Mounted at %s\n", status.MountPoint)
		fmt.Fprintln(&out, backupLine(status, now))
	}
	if r := status.Restore; r != nil {
		fmt.Fprintf(&out, "Restoring: %d of %d files, %s of %s%s\n", r.FilesDone, r.FilesTotal, api.FormatBytes(r.BytesDone),
			api.FormatBytes(r.BytesTotal), map[bool]string{true: " (paused)"}[r.Paused])
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
		return fmt.Sprintf("%d files not backed up yet · %s", status.NotBackedUpFiles, api.FormatBytes(status.NotBackedUpBytes))
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

// readPassword reads without echo. The caller sends the bytes, and the client wipes them.
// Without a terminal, one line is read from stdin so scripts can pipe the password in.
func (c *cli) readPassword(cmd *cobra.Command, prompt string) ([]byte, error) {
	fmt.Fprint(cmd.OutOrStdout(), prompt)
	defer fmt.Fprintln(cmd.OutOrStdout())
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return term.ReadPassword(int(os.Stdin.Fd()))
	}
	line, err := c.stdin.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return bytes.TrimRight(line, "\r\n"), nil
}

func (c *cli) readNewPassword(cmd *cobra.Command) ([]byte, error) {
	first, err := c.readPassword(cmd, "New vault password (at least 12 characters): ")
	if err != nil {
		return nil, err
	}
	second, err := c.readPassword(cmd, "Repeat the password: ")
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
