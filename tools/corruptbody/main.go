// Command corruptbody flips one byte of a body on Drive, for the acceptance drill that checks
// verify --deep names the damaged file. Without -id it lists the newest vault files.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/term"
	drivev3 "google.golang.org/api/drive/v3"
	"google.golang.org/api/option"

	"vault/internal/auth"
	"vault/internal/config"
	"vault/internal/crypto"
)

func main() {
	home := flag.String("home", "", "vault home directory")
	id := flag.String("id", "", "Drive ID of the body to corrupt")
	flag.Parse()
	if err := run(context.Background(), *home, *id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, home, id string) error {
	layout, err := config.HomeLayout(home)
	if err != nil {
		return err
	}
	service, err := connect(ctx, layout)
	if err != nil {
		return err
	}
	if id == "" {
		return listNewest(ctx, service)
	}
	return flipByte(ctx, service, id)
}

func connect(ctx context.Context, layout config.Layout) (*drivev3.Service, error) {
	data, err := os.ReadFile(layout.VaultHeaderFile)
	if err != nil {
		return nil, err
	}
	header, err := crypto.ParseVaultHeader(data)
	if err != nil {
		return nil, err
	}
	fmt.Fprint(os.Stderr, "Password: ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, err
	}
	keyring, err := crypto.UnlockVault(header, password)
	if err != nil {
		return nil, err
	}
	defer keyring.Destroy()
	token, err := auth.NewTokenStore(layout.TokenFile, keyring).Load()
	if err != nil {
		return nil, err
	}
	configDir, _ := os.UserConfigDir()
	credentials, err := auth.LoadClientCredentials(filepath.Join(configDir, "vault", "oauth_client.json"), auth.EmbeddedClientFile())
	if err != nil {
		return nil, err
	}
	return drivev3.NewService(ctx, option.WithTokenSource(auth.NewConfig(credentials).TokenSource(ctx, token)))
}

func listNewest(ctx context.Context, service *drivev3.Service) error {
	list, err := service.Files.List().Q("trashed = false and mimeType != 'application/vnd.google-apps.folder'").
		OrderBy("createdTime desc").PageSize(10).Fields("files(id, size, createdTime)").Context(ctx).Do()
	if err != nil {
		return err
	}
	for _, file := range list.Files {
		fmt.Printf("%s  %10d  %s\n", file.CreatedTime, file.Size, file.Id)
	}
	return nil
}

func flipByte(ctx context.Context, service *drivev3.Service, id string) error {
	response, err := service.Files.Get(id).Context(ctx).Download()
	if err != nil {
		return err
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("%s is empty", id)
	}
	body[len(body)/2] ^= 0xff
	if _, err := service.Files.Update(id, &drivev3.File{}).Media(bytes.NewReader(body)).Context(ctx).Do(); err != nil {
		return err
	}
	fmt.Printf("flipped byte %d of %d in %s\n", len(body)/2, len(body), id)
	return nil
}
