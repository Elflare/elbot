package app

import (
	"context"
	"errors"
)

// 本文件负责独立客户端运行，以及默认启动路径的客户端尝试与回退判断。
// 客户端通过平台线的公开能力构造和运行，不创建本地五条业务线总管。
// 实际回退到本地应用由 main 决定，本文件仅返回回退信号。

// ErrClientFallback 表示本次客户端尝试符合回退到本地启动的条件。
var ErrClientFallback = errors.New("client fallback")

// ClientOptions 是调用方传入的客户端启动参数。
type ClientOptions struct {
	ConfigPath string // 客户端使用的配置文件路径。
	ClientName string // 指定的客户端连接配置名称；为空时使用默认连接。
}

// RunClient 运行明确请求的独立客户端，连接失败直接返回错误，不回退到本地启动。
func RunClient(ctx context.Context, opts ClientOptions) error {
	// 加载客户端所需配置，通过平台线的公开能力构造并运行客户端。
	// 等待客户端结束，将运行结果交还调用方。
	pass
}

// TryRunClient 供默认启动路径尝试运行客户端，符合条件时返回 ErrClientFallback。
// 仅在未指定 ClientName 时，客户端被禁用、没有连接配置或默认本机服务拨号失败可回退。
// 指定连接、远端连接失败，以及配置解析、认证或握手错误均直接报告。
// 本函数不自行启动本地应用，也不将客户端正常结束视为回退。
func TryRunClient(ctx context.Context, opts ClientOptions) error {
	// 加载配置并通过平台线的公开能力尝试运行客户端。
	// 仅将符合已确认条件的失败转换为 ErrClientFallback，供 main 决定本地启动。
	// 正常结束或其他错误直接交还调用方。
	pass
}
