// Command vault controls the vault daemon from the terminal.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/cosmoboy/vault/internal/config"
	"github.com/spf13/cobra"
)

type cli struct {
	home   string
	client *api.Client
	stdin  *bufio.Reader
}

func main() {
	root := newRootCommand(&cli{stdin: bufio.NewReader(os.Stdin)})
	if err := root.ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "vault:", describe(err))
		os.Exit(1)
	}
}

func newRootCommand(c *cli) *cobra.Command {
	root := &cobra.Command{Use: "vault", Short: "Control the encrypted Drive-backed vault", SilenceUsage: true, SilenceErrors: true,
		PersistentPreRunE: func(*cobra.Command, []string) error { return c.connect() }}
	root.PersistentFlags().StringVar(&c.home, "home", "", "talk to the instance kept in this directory")
	root.AddCommand(c.statusCommand(), c.signInCommand(), c.createCommand(), c.joinCommand(), c.unlockCommand(), c.lockCommand(),
		c.syncCommand(), c.verifyCommand(), c.trashCommand(), c.downloadCommand(), c.freeCommand(), c.restoreCommand())
	return root
}

func (c *cli) connect() error {
	layout, err := config.ResolveLayout(c.home)
	if err != nil {
		return err
	}
	c.client = api.NewClient(layout.SocketPath)
	return nil
}

func describe(err error) string {
	switch {
	case errors.Is(err, api.ErrWrongPassword):
		return "Wrong password"
	case errors.Is(err, api.ErrLocked):
		return "the vault is locked; run vault unlock"
	case errors.Is(err, api.ErrNewerFormat):
		return "this vault was written by a newer version of Vault; update first"
	}
	return err.Error()
}
