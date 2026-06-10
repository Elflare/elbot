package builtin

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/config"
)

type configEnvDirKey struct{}

func WithConfigEnvDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, configEnvDirKey{}, dir)
}

func builtinEnv(ctx context.Context, key string) (string, error) {
	value, ok, err := optionalBuiltinEnv(ctx, key)
	if err != nil {
		return "", err
	}
	if ok {
		return value, nil
	}
	return "", fmt.Errorf("%s is required; set it in environment or config .env", key)
}

func optionalBuiltinEnv(ctx context.Context, key string) (string, bool, error) {
	value, ok, err := config.ConfigEnv(key, configEnvDir(ctx))
	if err != nil {
		return "", false, err
	}
	return value, ok && strings.TrimSpace(value) != "", nil
}

func configEnvDir(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	dir, _ := ctx.Value(configEnvDirKey{}).(string)
	return dir
}
