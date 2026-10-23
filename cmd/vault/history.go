package main

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"vault/internal/api"
)

func (c *cli) historyCommand() *cobra.Command {
	var filter, before string
	var limit int
	var ops bool
	command := &cobra.Command{Use: "history", Short: "Show the activity log, newest first", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			page, err := c.client.Commits(cmd.Context(), before, limit, filter)
			if err != nil {
				return err
			}
			printHistory(cmd.OutOrStdout(), page, ops)
			return nil
		}}
	command.Flags().StringVar(&filter, "path", "", "only commits that touched this path or folder")
	command.Flags().StringVar(&before, "before", "", "start below this clock (printed as the next page hint)")
	command.Flags().IntVarP(&limit, "limit", "n", 20, "number of commits, at most 200")
	command.Flags().BoolVar(&ops, "ops", false, "list each commit's operations")
	return command
}

func printHistory(out io.Writer, page api.CommitPage, ops bool) {
	for _, commit := range page.Commits {
		fmt.Fprintf(out, "%s  %-12s  %s\n", commit.Time.Local().Format("2006-01-02 15:04:05"), commit.DeviceName, commit.Summary)
		if !ops {
			continue
		}
		for _, op := range commit.Ops {
			if op.From != "" {
				fmt.Fprintf(out, "    %-10s %s -> %s\n", op.Kind, op.From, op.To)
			} else {
				fmt.Fprintf(out, "    %-10s %s\n", op.Kind, op.Path)
			}
		}
	}
	if page.NextBefore != "" && len(page.Commits) > 0 {
		fmt.Fprintf(out, "Older: vault history --before %s\n", page.NextBefore)
	}
}

func (c *cli) devicesCommand() *cobra.Command {
	command := simpleCommand("devices", "List the devices that use this vault", func(cmd *cobra.Command) error {
		devices, err := c.client.Devices(cmd.Context())
		if err != nil {
			return err
		}
		table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "ID\tNAME\tFIRST SEEN\t")
		for _, device := range devices {
			note := map[bool]string{true: "retired"}[device.Retired]
			if device.This {
				note = "this device"
			}
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", device.ID, device.Name, device.FirstSeen.Local().Format("2006-01-02"), note)
		}
		return table.Flush()
	})
	retire := &cobra.Command{Use: "retire <id>", Short: "Mark a device as no longer in use", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return c.client.RetireDevice(cmd.Context(), args[0]) }}
	command.AddCommand(retire)
	return command
}
