package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"vault/internal/api"
)

func (c *cli) settingsCommand() *cobra.Command {
	command := simpleCommand("settings", "Show the settings", func(cmd *cobra.Command) error {
		settings, err := c.client.Settings(cmd.Context())
		if err == nil {
			printSettings(cmd.OutOrStdout(), settings)
		}
		return err
	})
	set := &cobra.Command{Use: "set <key>=<value>...", Short: "Change settings; most apply at the next unlock",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			settings, err := c.client.Settings(cmd.Context())
			if err != nil {
				return err
			}
			for _, arg := range args {
				if err := applySetting(&settings, arg); err != nil {
					return err
				}
			}
			return c.client.UpdateSettings(cmd.Context(), settings)
		}}
	command.AddCommand(set)
	return command
}

func printSettings(out io.Writer, s api.Settings) {
	fmt.Fprintf(out, "mount_point=%s\ndevice_name=%s\nuploads=%d\ndownloads=%d\nactive_poll_seconds=%d\nidle_poll_seconds=%d\n"+
		"trash_retention_days=%d\nthumbnailer_deny_list=%s\n", s.MountPoint, s.DeviceName, s.Uploads, s.Downloads,
		s.ActivePollSeconds, s.IdlePollSeconds, s.TrashRetentionDays, strings.Join(s.ThumbnailerDenyList, ","))
}

func applySetting(s *api.Settings, assignment string) error {
	key, value, ok := strings.Cut(assignment, "=")
	if !ok {
		return fmt.Errorf("%q is not key=value", assignment)
	}
	numbers := map[string]*int{"uploads": &s.Uploads, "downloads": &s.Downloads, "active_poll_seconds": &s.ActivePollSeconds,
		"idle_poll_seconds": &s.IdlePollSeconds, "trash_retention_days": &s.TrashRetentionDays}
	switch key {
	case "mount_point":
		s.MountPoint = value
	case "device_name":
		s.DeviceName = value
	case "thumbnailer_deny_list":
		s.ThumbnailerDenyList = strings.FieldsFunc(value, func(r rune) bool { return r == ',' })
	default:
		target, known := numbers[key]
		if !known {
			return fmt.Errorf("unknown setting %q", key)
		}
		number, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s: %q is not a number", key, value)
		}
		*target = number
	}
	return nil
}

func (c *cli) signOutCommand() *cobra.Command {
	var yes bool
	command := simpleCommand("signout", "Sign out of Google; the vault locks", func(cmd *cobra.Command) error {
		if !yes && !confirm(cmd, "Sign out of Google? The vault locks and needs sign-in again. [y/N] ") {
			return nil
		}
		return c.client.SignOut(cmd.Context())
	})
	command.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return command
}
