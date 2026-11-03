package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSuperadminsNormalizeOnlyAtConfigurationEntry(t *testing.T) {
	cfg := &Config{Security: SecurityConfig{Superadmins: map[string][]string{
		PlatformTelegram: {" first ", "", "  ", "second", "first", "telegram:third"},
		PlatformQQOneBot: nil,
	}}}
	cfg.applyAppDefaults()
	want := []string{"first", "second", "first", "telegram:third"}
	if !reflect.DeepEqual(cfg.Security.Superadmins[PlatformTelegram], want) {
		t.Fatalf("administrator IDs=%q", cfg.Security.Superadmins[PlatformTelegram])
	}
	if len(cfg.Security.Superadmins[PlatformQQOneBot]) != 0 {
		t.Fatal("empty administrator list acquired a default")
	}
	cfg.applyAppDefaults()
	if !reflect.DeepEqual(cfg.Security.Superadmins[PlatformTelegram], want) {
		t.Fatal("repeated configuration normalization changed IDs")
	}
}

func TestConfigEnvUsesItsOwnConfigurationLocation(t *testing.T) {
	const key = "ELBOT_PLATFORM_ENV_LOCATION_TEST"
	t.Setenv(key, "")
	for _, value := range []string{"first", "second"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(key+"="+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cfg := &Config{ConfigPath: filepath.Join(dir, "app.toml")}
		lookup := cfg.ConfigEnv
		got, found, err := lookup(key)
		if err != nil || !found || got != value {
			t.Fatalf("lookup=%q,%v,%v", got, found, err)
		}
	}
}
