// Package config loads and validates config.toml and resolves where an instance keeps its files.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/cosmoboy/vault/internal/procfilter"
)

// Duration marshals with a value receiver so it encodes as text inside a Config value;
// unmarshalling needs a pointer.
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	parsed, err := time.ParseDuration(string(text))
	d.Duration = parsed
	return err
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.String()), nil
}

type Workers struct {
	Uploads   int `toml:"uploads"`
	Downloads int `toml:"downloads"`
	Hashing   int `toml:"hashing"`
}

type Polling struct {
	Active         Duration `toml:"active"`
	Idle           Duration `toml:"idle"`
	ActivityWindow Duration `toml:"activity_window"`
}

type Config struct {
	MountPoint           string   `toml:"mount_point"`
	DeviceName           string   `toml:"device_name"`
	DriveFolderID        string   `toml:"drive_folder_id"`
	OAuthClientFile      string   `toml:"oauth_client_file"`
	Workers              Workers  `toml:"workers"`
	Polling              Polling  `toml:"polling"`
	TrashRetentionDays   int      `toml:"trash_retention_days"`
	ThumbnailerDenyList  []string `toml:"thumbnailer_deny_list"`
	LockAllMemory        bool     `toml:"lock_all_memory"`
	QuietPeriod          Duration `toml:"quiet_period"`
	OpenDirtyMaxAge      Duration `toml:"open_dirty_max_age"`
	DeleteHold           Duration `toml:"delete_hold"`
	MaxDeleteHold        Duration `toml:"max_delete_hold"`
	LockFlushTimeout     Duration `toml:"lock_flush_timeout"`
	UnlockCatchUpTimeout Duration `toml:"unlock_catchup_timeout"`
}

func Defaults(home string) Config {
	hostname, _ := os.Hostname()
	return Config{
		MountPoint:           filepath.Join(home, "Vault"),
		DeviceName:           hostname,
		Workers:              Workers{Uploads: 4, Downloads: 4, Hashing: 2},
		Polling:              Polling{Active: seconds(10), Idle: seconds(30), ActivityWindow: seconds(300)},
		TrashRetentionDays:   30,
		ThumbnailerDenyList:  procfilter.DefaultDenyList(),
		QuietPeriod:          seconds(2),
		OpenDirtyMaxAge:      seconds(60),
		DeleteHold:           seconds(3),
		MaxDeleteHold:        seconds(30),
		LockFlushTimeout:     seconds(20),
		UnlockCatchUpTimeout: seconds(15),
	}
}

// Load reads path over the defaults. A missing file means defaults; unknown keys are errors,
// so a typo cannot silently fall back to a default.
func Load(path string, defaults Config) (Config, error) {
	config := defaults
	metadata, err := toml.DecodeFile(path, &config)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, key := range undecoded {
			keys[i] = key.String()
		}
		return Config{}, fmt.Errorf("%s: unknown settings %s", path, strings.Join(keys, ", "))
	}
	return config, config.Validate()
}

func Save(path string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	var buffer bytes.Buffer
	if err := toml.NewEncoder(&buffer).Encode(config); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, buffer.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (c *Config) Validate() error {
	var problems []string
	if !filepath.IsAbs(c.MountPoint) {
		problems = append(problems, "mount_point must be an absolute path")
	}
	if strings.TrimSpace(c.DeviceName) == "" {
		problems = append(problems, "device_name is empty")
	}
	for name, count := range map[string]int{"uploads": c.Workers.Uploads, "downloads": c.Workers.Downloads, "hashing": c.Workers.Hashing} {
		if count < 1 || count > 64 {
			problems = append(problems, fmt.Sprintf("workers.%s must be 1..64", name))
		}
	}
	if c.TrashRetentionDays < 1 {
		problems = append(problems, "trash_retention_days must be at least 1")
	}
	if c.Polling.Active.Duration <= 0 || c.Polling.Idle.Duration < c.Polling.Active.Duration {
		problems = append(problems, "polling.idle must be at least polling.active, and both positive")
	}
	if c.MaxDeleteHold.Duration < c.DeleteHold.Duration {
		problems = append(problems, "max_delete_hold must be at least delete_hold")
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid config: %s", strings.Join(problems, "; "))
	}
	return nil
}

func seconds(n int) Duration {
	return Duration{time.Duration(n) * time.Second}
}
