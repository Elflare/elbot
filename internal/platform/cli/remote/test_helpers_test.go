package remote

import (
	"path/filepath"

	"elbot/internal/config"
)

const testPlatformName = "cli"

func testConfigEnv(dir string) func(string) (string, bool, error) {
	return (&config.Config{ConfigPath: filepath.Join(dir, "app.toml")}).ConfigEnv
}
