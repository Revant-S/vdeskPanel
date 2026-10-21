package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/spf13/cobra"
)

func (c *cli) statusCommand() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show whether everything is backed up", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := c.client.Status(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), formatStatus(status, time.Now()))
			return nil
		}}
}

func (c *cli) signInCommand() *cobra.Command {
	return &cobra.Command{Use: "signin", Short: "Sign in to Google in the browser", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			authURL, err := c.client.BeginSignIn(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Open this address to sign in to Google:\n"+authURL)
			exec.Command("xdg-open", authURL).Start()
			return nil
		}}
}

func (c *cli) createCommand() *cobra.Command {
	var deviceName string
	command := &cobra.Command{Use: "create", Short: "Create a new vault on this Google account", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "A lost password cannot be recovered. Keep it somewhere other than this laptop.")
			password, err := c.readNewPassword(cmd)
			if err != nil {
				return err
			}
			return c.client.CreateVault(cmd.Context(), password, deviceName)
		}}
	command.Flags().StringVar(&deviceName, "device-name", "", "name for this device (default: hostname)")
	return command
}

func (c *cli) joinCommand() *cobra.Command {
	var deviceName string
	var onDemand bool
	command := &cobra.Command{Use: "join", Short: "Join the vault on this Google account", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, err := c.readPassword(cmd, "Vault password: ")
			if err != nil {
				return err
			}
			mode := api.JoinRestoreEverything
			if onDemand {
				mode = api.JoinOnDemand
			}
			return c.client.JoinVault(cmd.Context(), password, deviceName, mode)
		}}
	command.Flags().StringVar(&deviceName, "device-name", "", "name for this device (default: hostname)")
	command.Flags().BoolVar(&onDemand, "on-demand", false, "download files only when opened, instead of restoring everything")
	return command
}

func (c *cli) unlockCommand() *cobra.Command {
	return &cobra.Command{Use: "unlock", Short: "Unlock and mount the vault", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			password, err := c.readPassword(cmd, "Vault password: ")
			if err != nil {
				return err
			}
			return c.client.Unlock(cmd.Context(), password)
		}}
}

func (c *cli) lockCommand() *cobra.Command {
	return simpleCommand("lock", "Unmount the vault and forget the keys", func(cmd *cobra.Command) error { return c.client.Lock(cmd.Context()) })
}

func (c *cli) syncCommand() *cobra.Command {
	return simpleCommand("sync", "Upload pending changes and fetch remote ones now", func(cmd *cobra.Command) error { return c.client.SyncNow(cmd.Context()) })
}

func (c *cli) verifyCommand() *cobra.Command {
	var deep bool
	command := &cobra.Command{Use: "verify", Short: "Check that every file is safely on Drive", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			report, err := c.client.Verify(cmd.Context(), deep)
			if err != nil {
				return err
			}
			for _, problem := range report.Problems {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", problem.Path, problem.Kind)
			}
			if len(report.Problems) > 0 {
				return fmt.Errorf("%d problems found", len(report.Problems))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "All %d files verified.\n", report.FilesChecked)
			return nil
		}}
	command.Flags().BoolVar(&deep, "deep", false, "also download and decrypt every file")
	return command
}

func (c *cli) downloadCommand() *cobra.Command {
	return pathCommand(c, "download", "Keep a file or folder on this device", func(cmd *cobra.Command, path string) error { return c.client.Download(cmd.Context(), path) })
}

func (c *cli) freeCommand() *cobra.Command {
	return pathCommand(c, "free", "Free up space by keeping a file or folder only on Drive", func(cmd *cobra.Command, path string) error { return c.client.Free(cmd.Context(), path) })
}

func (c *cli) restoreCommand() *cobra.Command {
	command := &cobra.Command{Use: "restore", Short: "Pause or resume restoring files to this device"}
	command.AddCommand(simpleCommand("pause", "Pause the restore", func(cmd *cobra.Command) error { return c.client.PauseRestore(cmd.Context(), true) }),
		simpleCommand("resume", "Resume the restore", func(cmd *cobra.Command) error { return c.client.PauseRestore(cmd.Context(), false) }))
	return command
}

func simpleCommand(use, short string, run func(*cobra.Command) error) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return run(cmd) }}
}

// pathCommand accepts paths inside the vault ("/Photos") or real paths under the mount point,
// which is what file manager scripts pass.
func pathCommand(c *cli, use, short string, run func(*cobra.Command, string) error) *cobra.Command {
	return &cobra.Command{Use: use + " <path>...", Short: short, Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := c.client.Status(cmd.Context())
			if err != nil {
				return err
			}
			for _, arg := range args {
				if err := run(cmd, vaultPath(arg, status.MountPoint)); err != nil {
					return fmt.Errorf("%s: %w", arg, err)
				}
			}
			return nil
		}}
}

func vaultPath(arg, mountPoint string) string {
	if mountPoint != "" && (arg == mountPoint || strings.HasPrefix(arg, mountPoint+"/")) {
		return "/" + strings.TrimPrefix(strings.TrimPrefix(arg, mountPoint), "/")
	}
	return arg
}
