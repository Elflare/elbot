package app

import (
	"context"
	"errors"
	"fmt"

	"elbot/internal/config"
	"elbot/internal/platform"
)

// ErrClientFallback 由 main 处理；App 客户端入口不自行启动本地应用。
var ErrClientFallback = errors.New("client fallback")

type ClientOptions struct {
	ConfigPath string // 客户端使用的配置路径。
	ClientName string // 指定连接名称；为空时使用默认连接。
}

// RunClient 运行明确请求的客户端，任何失败直接交还调用方。
func RunClient(ctx context.Context, opts ClientOptions) error {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load client config: %w", err)
	}
	// 显式启动不回退。
	return platform.RunClient(ctx, cfg, opts.ClientName).Err
}

// TryRunClient 根据 Platform 的分类结果决定是否返回回退信号。
func TryRunClient(ctx context.Context, opts ClientOptions) error {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load client config: %w", err)
	}
	// 仅在 Platform 判定可回退时通知调用方。
	result := platform.RunClient(ctx, cfg, opts.ClientName)
	if result.Kind == platform.ClientFallback {
		return ErrClientFallback
	}
	return result.Err
}
