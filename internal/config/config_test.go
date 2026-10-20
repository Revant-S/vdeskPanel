package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMissingFileGivesDefaults(t *testing.T) {
	defaults := Defaults("/home/test")
	config, err := Load(filepath.Join(t.TempDir(), "absent.toml"), defaults)
	if err != nil || config.MountPoint != "/home/test/Vault" || config.Workers.Uploads != 4 || config.QuietPeriod.Duration != 2*time.Second {
		t.Fatalf("Load = %+v, %v", config, err)
	}
}

func TestFileOverridesDefaultsAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	os.WriteFile(path, []byte("mount_point = \"/mnt/vault-b\"\ndevice_name = \"amber\"\nquiet_period = \"500ms\"\n[workers]\nuploads = 8\n"), 0o600)
	config, err := Load(path, Defaults("/home/test"))
	if err != nil {
		t.Fatal(err)
	}
	if config.MountPoint != "/mnt/vault-b" || config.Workers.Uploads != 8 || config.Workers.Downloads != 4 || config.QuietPeriod.Duration != 500*time.Millisecond {
		t.Fatalf("config = %+v", config)
	}
	if err := Save(path, config); err != nil {
		t.Fatal(err)
	}
	again, err := Load(path, Defaults("/elsewhere"))
	if err != nil || again.MountPoint != "/mnt/vault-b" || again.DeviceName != "amber" {
		t.Fatalf("after save = %+v, %v", again, err)
	}
}

func TestUnknownKeysAndBadValuesAreRejected(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"typo":          "mount_pont = \"/x\"\n",
		"relative":      "mount_point = \"Vault\"\n",
		"zero uploads":  "[workers]\nuploads = 0\n",
		"bad duration":  "quiet_period = \"soon\"\n",
		"hold inverted": "delete_hold = \"10s\"\nmax_delete_hold = \"1s\"\n",
	}
	for name, body := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".toml")
		os.WriteFile(path, []byte(body), 0o600)
		if _, err := Load(path, Defaults("/home/test")); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHomeLayoutKeepsEverythingUnderHome(t *testing.T) {
	home := t.TempDir()
	layout, err := HomeLayout(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{layout.ConfigFile, layout.IndexFile, layout.CacheDir, layout.TokenFile, layout.SocketPath, layout.DeviceIDFile, layout.LogFile, layout.VaultHeaderFile} {
		if !strings.HasPrefix(path, home+string(filepath.Separator)) {
			t.Errorf("%s is outside home", path)
		}
	}
	if err := layout.CreateDirs(); err != nil {
		t.Fatal(err)
	}
	if _, err := HomeLayout("/" + strings.Repeat("x", 120)); err == nil {
		t.Fatal("over-long socket path accepted")
	}
}
