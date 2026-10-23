// Command vault-panel is the desktop control panel: a Wails window over the daemon API.
package main

import (
	"embed"
	"flag"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/cosmoboy/vault/internal/api"
	"github.com/cosmoboy/vault/internal/config"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	home := flag.String("home", "", "talk to the instance kept in this directory")
	flag.Parse()
	layout, err := config.ResolveLayout(*home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vault-panel:", err)
		os.Exit(1)
	}
	app := NewApp(api.NewClient(layout.SocketPath))
	err = wails.Run(&options.App{
		Title:       "Vault",
		Width:       1000,
		Height:      680,
		MinWidth:    640,
		MinHeight:   420,
		AssetServer: &assetserver.Options{Assets: assets},
		OnStartup:   app.startup,
		Bind:        []any{app},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "vault-panel:", err)
		os.Exit(1)
	}
}
