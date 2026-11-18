// Package weave runs one Python script with asynchronous ElBot tool proxies.
package weave

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"elbot/internal/operationresult"
	"elbot/internal/processenv"
	"elbot/internal/tool/share"
	"elbot/internal/workspace"
)

//go:embed runner.py
var runner string

type args struct {
	Code     string
	Mode     string
	Packages []string
	Timeout  int64
}

type pipes struct {
	requests     io.Reader // Python 提交的工具调用。
	responses    io.Writer // Go 发送的初始化消息和结果。
	connect      func(context.Context) error
	releaseChild func() // 启动后关闭父进程中多余的子端副本。
	close        func()
}

const fallback = "本次已自动回退到系统 Python。"

func Definition(env processenv.Environment) share.ToolDefinition {
	return share.NewBuilder("tool_weave").
		Description(`执行py代码，也可编排工具，支持批量、并发和结果处理。简单调用别用该工具。例：
import asyncio
from pathlib import Path
projects = {"Go": "go.dev", "Rust": "blog.rust-lang.org", "Python": "blog.python.org"}
news = await asyncio.gather(*(web_search(query=f"{name} release", include_domains=[site], time_range="week", max_results=3) for name, site in projects.items()))
report = "\n\n".join(f"## {name}\n{text}" for name, text in zip(projects, news))
Path("weekly.md").write_text(report, encoding="utf-8")
print(await send_file(source="weekly.md"))`).
		Risk(share.RiskHigh).
		Tags("agent", "files").
		String("code", "Python 源码，支持顶层 await，用 print 输出。", share.Required()).
		String("mode", "省略：项目 uv，否则系统 Python。显式 uv：无项目用临时环境，准备失败回退系统 Python。", share.Enum("system", "uv")).
		StringArray("packages", "临时依赖，仅 uv。").
		Integer("timeout", "总超时，秒，默认 10。").
		Build(func(ctx context.Context, req share.CallRequest, info share.Info) (operationresult.Result[*share.PreparedCall], error) {
			var a args
			if err := json.Unmarshal(req.Arguments, &a); err != nil {
				return operationresult.Result[*share.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
			}
			if strings.TrimSpace(a.Code) == "" || (a.Mode != "" && a.Mode != "system" && a.Mode != "uv") || a.Timeout < 0 || a.Timeout > int64((1<<63-1)/time.Second) {
				return operationresult.Result[*share.PreparedCall]{Status: operationresult.InvalidArguments, Message: "tool_weave 参数无效，请检查 code、mode、timeout"}, nil
			}
			if a.Timeout == 0 {
				a.Timeout = 10
			}
			dir, err := workspace.CurrentWorkspaceDir(ctx)
			if err != nil {
				return operationresult.Result[*share.PreparedCall]{}, err
			}
			details := []share.ConfirmationDetailBlock{
				share.CodeConfirmationDetail("python", a.Code),
				share.FieldsConfirmationDetail(
					share.ConfirmationField{Key: "work_dir", Label: "工作目录", Value: dir},
					share.ConfirmationField{Key: "mode", Label: "Python 模式（空为自动）", Value: a.Mode},
					share.ConfirmationField{Key: "packages", Label: "可能解析或下载的临时依赖", Value: a.Packages},
					share.ConfirmationField{Key: "timeout", Label: "超时（秒）", Value: a.Timeout},
				),
			}
			return operationresult.Result[*share.PreparedCall]{Status: operationresult.Success, Data: share.NewPreparedCall(req, share.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (operationresult.Result[*share.Result], string, error) {
				return run(ctx, env, dir, a)
			})}, nil
		})
}

// uvOptions 只选择运行环境。返回空参数表示使用系统 Python。
func uvOptions(ctx context.Context, env processenv.Environment, dir string, a args) ([]string, []string) {
	if a.Mode == "system" {
		return nil, nil
	}
	probe := env.CommandContext(ctx, "uv", "workspace", "dir")
	probe.Dir = dir
	err := probe.Run()
	options := []string{"run", "--quiet"}
	var warnings []string
	var exit *exec.ExitError
	switch {
	case err == nil:
		options = append(options, "--no-sync")
	case a.Mode == "uv" && errors.As(err, &exit):
		options = append(options, "--no-project", "--isolated")
		warnings = []string{"当前目录不在 uv 项目中，已自动使用临时 uv 环境。"}
	default:
		return nil, []string{fallback}
	}
	for _, pkg := range a.Packages {
		options = append(options, "--with="+pkg)
	}
	return append(options, "--", "python"), warnings
}

func run(parent context.Context, env processenv.Environment, dir string, a args) (operationresult.Result[*share.Result], string, error) {
	ctx, cancel := context.WithTimeout(parent, time.Duration(a.Timeout)*time.Second)
	defer cancel()
	options, warnings := uvOptions(ctx, env, dir, a)
	var stdout, stderr share.ProcessOutput
	var dispatched bool // 开始向就绪的 runner 交付代码后，不可再次执行。
	execute := func(name string, prefix []string) error {
		cmd := env.CommandContext(ctx, name, append(prefix, "-X", "utf8", "-u", "-c", runner)...)
		cmd.Dir = dir
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.WaitDelay = time.Second
		return runProcess(ctx, cmd, a.Code, share.WeaveFromContext(ctx), &dispatched)
	}
	var err error
	if options != nil {
		err = execute("uv", options)
		var exit *exec.ExitError
		if !dispatched && ctx.Err() == nil && (errors.As(err, &exit) || errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)) {
			reason := strings.TrimSpace(stderr.String())
			if reason == "" {
				reason = err.Error()
			}
			warnings = append(warnings, "uv 环境准备失败："+reason+"。"+fallback)
			options = nil
			stdout, stderr = share.ProcessOutput{}, share.ProcessOutput{}
		}
	}
	if options == nil && ctx.Err() == nil {
		if len(a.Packages) > 0 {
			message := share.AppendWarnings("packages 需要 uv 环境，脚本未执行", warnings)
			return operationresult.Result[*share.Result]{Status: operationresult.InvalidArguments, Message: message}, "Python 依赖需要 uv", nil
		}
		python := "python3"
		if runtime.GOOS == "windows" {
			python = "python"
		}
		err = execute(python, nil)
	}
	content := stdout.String()
	if text := stderr.String(); text != "" {
		content += "\nstderr:\n" + text
	}
	if err != nil || ctx.Err() != nil {
		status := operationresult.Failed
		if ctx.Err() != nil {
			err = ctx.Err()
			status = operationresult.Canceled
			if errors.Is(err, context.DeadlineExceeded) {
				status = operationresult.Timeout
			}
		}
		message := share.AppendWarnings(strings.TrimSpace(fmt.Sprintf("tool_weave: %v\n%s", err, content)), warnings)
		var exit *exec.ExitError
		if status == operationresult.Failed && !errors.As(err, &exit) {
			return operationresult.Result[*share.Result]{}, message, errors.New(message)
		}
		return operationresult.Result[*share.Result]{Status: status, Message: message}, "Python 执行失败", nil
	}
	return operationresult.Result[*share.Result]{Status: operationresult.Success, Data: &share.Result{Content: content, Warnings: warnings}}, "Python 执行完成", nil
}

func runProcess(ctx context.Context, cmd *exec.Cmd, code string, w share.WeaveContext, dispatched *bool) error {
	p, err := openPipes(cmd)
	if err != nil {
		return err
	}
	defer p.close()
	err = cmd.Start()
	p.releaseChild()
	if err != nil {
		return err
	}
	serviceCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(serviceCtx, p, code, w, dispatched) }()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case err = <-exited:
		cancel()
		p.close()
		pipeErr := <-done
		if err == nil && !errors.Is(pipeErr, context.Canceled) && !errors.Is(pipeErr, io.ErrClosedPipe) && !errors.Is(pipeErr, os.ErrClosed) {
			err = pipeErr
		}
	case pipeErr := <-done:
		if pipeErr != nil {
			_ = cmd.Cancel()
		}
		err = <-exited
		if err == nil {
			err = pipeErr
		}
	}
	return err
}

func serve(ctx context.Context, p *pipes, code string, w share.WeaveContext, dispatched *bool) error {
	if err := p.connect(ctx); err != nil {
		return err
	}
	// runner 就绪后才交付用户代码，uv 准备失败可以安全回退。
	var ready [1]byte
	if _, err := io.ReadFull(p.requests, ready[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	enc := json.NewEncoder(p.responses)
	if w.Names == nil {
		w.Names = []string{}
	}
	*dispatched = true
	if err := enc.Encode(map[string]any{"code": code, "names": w.Names}); err != nil {
		return err
	}
	var workers sync.WaitGroup
	ctx, cancel := context.WithCancel(ctx)
	defer workers.Wait()
	defer cancel()
	var write sync.Mutex
	dec := json.NewDecoder(p.requests)
	for {
		var req struct {
			ID   uint64
			Name string
			Args json.RawMessage
		}
		err := dec.Decode(&req)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		workers.Go(func() {
			result, err := w.Call(ctx, req.Name, req.Args)
			ok := err == nil && result.Status == operationresult.Success
			text := result.Data
			if err != nil {
				text = err.Error()
			} else if !ok {
				text = result.Message
			}
			write.Lock()
			defer write.Unlock()
			if err := enc.Encode(map[string]any{"id": req.ID, "ok": ok, "text": text}); err != nil {
				cancel()
			}
		})
	}
}
