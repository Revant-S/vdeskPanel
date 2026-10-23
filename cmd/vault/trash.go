package main

import (
	"bufio"
	"fmt"
	"strings"
	"text/tabwriter"
	"vault/internal/api"

	"github.com/spf13/cobra"
)

func (c *cli) trashCommand() *cobra.Command {
	command := &cobra.Command{Use: "trash", Short: "List, restore or delete trash items"}
	list := simpleCommand("ls", "List trash items", func(cmd *cobra.Command) error {
		items, err := c.client.Trash(cmd.Context())
		if err != nil {
			return err
		}
		table := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "ID\tORIGINAL PATH\tREASON\tDEVICE\tDELETED\tEXPIRES")
		for _, item := range items {
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", item.ID, item.OriginalPath, item.Reason, item.DeviceName,
				item.DeletedAt.Format("2006-01-02 15:04"), item.ExpiresAt.Format("2006-01-02"))
		}
		return table.Flush()
	})
	restore := &cobra.Command{Use: "restore <id>", Short: "Put a trash item back", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			restored, err := c.client.RestoreTrash(cmd.Context(), args[0])
			if err == nil {
				fmt.Fprintln(cmd.OutOrStdout(), "Restored to", restored)
			}
			return err
		}}
	purge := &cobra.Command{Use: "purge <id>", Short: "Delete a trash item permanently", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return c.client.PurgeTrash(cmd.Context(), args[0]) }}
	command.AddCommand(list, restore, purge, c.emptyTrashCommand())
	return command
}

func (c *cli) emptyTrashCommand() *cobra.Command {
	var yes bool
	command := &cobra.Command{Use: "empty", Short: "Delete every trash item permanently", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			items, err := c.client.Trash(cmd.Context())
			if err != nil {
				return err
			}
			var size int64
			for _, item := range items {
				size += item.Size
			}
			if !yes && !confirm(cmd, fmt.Sprintf("Permanently delete %d items (%s)? [y/N] ", len(items), api.FormatBytes(size))) {
				return nil
			}
			result, err := c.client.EmptyTrash(cmd.Context())
			if err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "Deleted %d items.\n", result.Items)
			}
			return err
		}}
	command.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return command
}

func confirm(cmd *cobra.Command, question string) bool {
	fmt.Fprint(cmd.OutOrStdout(), question)
	answer, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}
