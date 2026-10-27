// client.go 选择客户端连接并分类运行结果，不启动本地应用。
package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

type ClientResultKind uint8

const (
	ClientCompleted ClientResultKind = iota // 用户正常退出。
	ClientFallback                          // 默认连接不可用，允许入口尝试本地运行。
	ClientFailed                            // 其他失败，不允许本地回退。
)

type ClientResult struct {
	Kind ClientResultKind // 客户端运行结果的分类。
	Err  error            // 原始失败或回退原因；正常结束为 nil。
}

func RunClient(ctx context.Context, rawConfig map[string]any, configDir, name string) ClientResult {
	name = strings.TrimSpace(name)
	if err := ctx.Err(); err != nil {
		return ClientResult{Kind: ClientFailed, Err: err}
	}
	cfg, err := NewConfigFromPlatformConfig(rawConfig)
	if err != nil {
		return ClientResult{Kind: ClientFailed, Err: err}
	}
	if !cfg.Enabled {
		err = fmt.Errorf("cli platform is disabled: %w", ErrClientNotConfigured)
		return classifyClientResult(name, "", err)
	}
	client, err := NewRemoteClient(RemoteClientOptions{Config: cfg, ConfigDir: configDir, ClientName: name})
	if err != nil {
		return classifyClientResult(name, "", err)
	}
	return classifyClientResult(name, client.URL(), client.Run(ctx))
}

func classifyClientResult(name, address string, err error) ClientResult {
	if err == nil {
		return ClientResult{Kind: ClientCompleted}
	}
	kind := ClientFailed
	if name == "" && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		if errors.Is(err, ErrClientNotConfigured) || (errors.Is(err, ErrServerUnavailable) && isLocalCLIURL(address)) {
			kind = ClientFallback
		}
	}
	return ClientResult{Kind: kind, Err: err}
}

func isLocalCLIURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
