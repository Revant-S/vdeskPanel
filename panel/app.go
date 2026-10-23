package main

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"path/filepath"

	"github.com/godbus/dbus/v5"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"vault/internal/api"
	"vault/panel/layout"
)

const historyPageSize = 200

// App is what the frontend calls. Every method is a thin pass-through to the daemon, so the
// panel holds no state the daemon does not.
type App struct {
	ctx    context.Context
	client *api.Client
}

type HistoryPage struct {
	Rows       []layout.Row     `json:"rows"`
	Devices    []api.DeviceView `json:"devices"`
	NextBefore string           `json:"nextBefore"`
}

func NewApp(client *api.Client) *App {
	return &App{client: client}
}

// startup forwards the daemon's live status to the frontend for as long as the window lives.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go func() {
		for event := range a.client.Subscribe(ctx) {
			if event.Status != nil {
				runtime.EventsEmit(ctx, "status", event.Status)
			}
		}
	}()
}

func (a *App) Status() (api.Status, error) {
	return a.client.Status(a.ctx)
}

// Unlock takes the password as a JS string, which cannot be wiped (R20); the bytes handed
// to the daemon are.
func (a *App) Unlock(password string) error {
	return describe(a.client.Unlock(a.ctx, []byte(password)))
}

func (a *App) Lock() error {
	return describe(a.client.Lock(a.ctx))
}

func (a *App) Tree(folder string) ([]api.TreeEntry, error) {
	entries, err := a.client.Tree(a.ctx, folder)
	return entries, describe(err)
}

func (a *App) History(before, filter string) (HistoryPage, error) {
	page, err := a.client.Commits(a.ctx, before, historyPageSize, filter)
	if err != nil {
		return HistoryPage{}, describe(err)
	}
	return HistoryPage{Rows: layout.AssignLanes(page.Commits, page.Devices), Devices: page.Devices, NextBefore: page.NextBefore}, nil
}

func (a *App) Trash() ([]api.TrashItem, error) {
	items, err := a.client.Trash(a.ctx)
	return items, describe(err)
}

func (a *App) RestoreTrash(id string) (string, error) {
	restored, err := a.client.RestoreTrash(a.ctx, id)
	return restored, describe(err)
}

func (a *App) PurgeTrash(id string) error {
	return describe(a.client.PurgeTrash(a.ctx, id))
}

func (a *App) EmptyTrash() (api.EmptyTrashResult, error) {
	result, err := a.client.EmptyTrash(a.ctx)
	return result, describe(err)
}

func (a *App) Download(vaultPath string) error {
	return describe(a.client.Download(a.ctx, vaultPath))
}

func (a *App) Free(vaultPath string) error {
	return describe(a.client.Free(a.ctx, vaultPath))
}

func (a *App) Settings() (api.Settings, error) {
	settings, err := a.client.Settings(a.ctx)
	return settings, describe(err)
}

func (a *App) UpdateSettings(settings api.Settings) error {
	return describe(a.client.UpdateSettings(a.ctx, settings))
}

func (a *App) SignOut() error {
	return describe(a.client.SignOut(a.ctx))
}

func (a *App) Open(vaultPath string) error {
	local, err := a.localPath(vaultPath)
	if err != nil {
		return err
	}
	return exec.Command("xdg-open", local).Start()
}

// ShowInFiles asks the file manager to open the item's folder with the item selected.
func (a *App) ShowInFiles(vaultPath string) error {
	local, err := a.localPath(vaultPath)
	if err != nil {
		return err
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	uri := (&url.URL{Scheme: "file", Path: local}).String()
	return conn.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1").
		Call("org.freedesktop.FileManager1.ShowItems", 0, []string{uri}, "").Err
}

func (a *App) localPath(vaultPath string) (string, error) {
	status, err := a.client.Status(a.ctx)
	if err != nil {
		return "", describe(err)
	}
	if status.State != api.StateMounted && status.State != api.StateRestoring {
		return "", errors.New("the vault is not mounted")
	}
	return filepath.Join(status.MountPoint, filepath.FromSlash(vaultPath)), nil
}

// describe turns daemon errors into the sentences the panel shows.
func describe(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, api.ErrWrongPassword):
		return errors.New("wrong password")
	case errors.Is(err, api.ErrLocked):
		return errors.New("the vault is locked")
	case errors.Is(err, api.ErrNewerFormat):
		return errors.New("this vault was written by a newer version of Vault; update first")
	}
	return err
}
