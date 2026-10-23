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
	// Platform.RunClient(ctx, cfg, name) 负责连接选择、运行及结果分类。
	// 返回 ClientResult{Kind, Err}：ClientCompleted 的 Err 为 nil，
	// ClientFallback、ClientFailed 均保留原始错误；明确运行不触发回退。
	result := platform.RunClient(ctx, cfg, opts.ClientName)
	return result.Err
}

// TryRunClient 根据 Platform 的分类结果决定是否返回回退信号。
func TryRunClient(ctx context.Context, opts ClientOptions) error {
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load client config: %w", err)
	}
	// Platform 仅将默认连接未配置、被禁用或本机服务拨号失败归为 ClientFallback。
	// 指定连接、远端失败、配置、认证、握手及取消均不归为可回退；App 不重复判断。
	result := platform.RunClient(ctx, cfg, opts.ClientName)
	switch result.Kind {
	case platform.ClientFallback:
		return ErrClientFallback
	default:
		// ClientCompleted 返回 nil，ClientFailed 返回原始错误。
		return result.Err
	}
}
