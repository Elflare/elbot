package qqofficial

import (
	"elbot/internal/config"
	"elbot/internal/platform/share"
	"path/filepath"
	"testing"
)

const testPlatformName = "qqofficial"

func testConfigEnv(dir string) func(string) (string, bool, error) {
	return (&config.Config{ConfigPath: filepath.Join(dir, "app.toml")}).ConfigEnv
}

func testCommonConfig(t *testing.T) share.CommonConfig {
	t.Helper()
	return share.CommonConfig{PlatformName: testPlatformName, Superadmins: share.NewSuperadmins(testPlatformName, nil), ConfigEnv: testConfigEnv(t.TempDir())}
}
