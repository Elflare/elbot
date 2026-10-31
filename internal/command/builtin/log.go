package builtin

import (
	"context"
	"fmt"
	"strings"

	"elbot/internal/command"
	"elbot/internal/events"
	"elbot/internal/logging"
)

const defaultLogListLimit = 5

func auditInfo() command.Info {
	return command.Info{
		Name:        "audit",
		Args:        "[options]",
		Description: "审计日志",
		Help: strings.TrimSpace(`-n, --limit <n>       条数，默认 5
--days <n>            天数，默认 1
--level <level>       最低 debug|info|warn|error，默认 debug
-d/-i/-w/-e           级别简写
--raw                原始记录
-t, --tool [name]     工具开始/结束，可指定名称
--run_source <name>   foreground|cron|elnis
--hook               Hook 日志
--since/--until <time>  时间范围，如 2h、2026-06-03
--event <name>        事件
--risk <level>        风险
--actor <id>          用户
--session <id>        会话
--msg <text>          正文过滤
--contains <text>     正文及原始记录搜索`),
	}
}

func NewAudit(deps Deps) command.Handler {
	return logCommand{deps: deps, info: auditInfo(), audit: true}
}

func elwispInfo() command.Info {
	return command.Info{
		Name:        "elwisp",
		Args:        "[name] [options]",
		Description: "Elwisp 日志",
		Help: strings.TrimSpace(`[name], --name/--elwisp <name>  Elwisp 名称
-n, --limit <n>       条数，默认 5
--days <n>            天数，默认 1
--level <level>       最低 debug|info|warn|error，默认 debug
-d/-i/-w/-e           级别简写
--raw                原始记录
--since/--until <time>  时间范围，如 2h、2026-06-03
--source <source>    来源
--id/--source-id <id> 外部来源 ID
--mode <mode>        record|direct|llm
--event-key <key>    事件 key
--event-id <id>      内部事件 ID
--token <name>       token 名
--tag <tag>          标签，可重复
--msg <text>         正文过滤
--contains <text>    正文及原始记录搜索`),
	}
}

func NewElwisp(deps Deps) command.Handler {
	return logCommand{deps: deps, info: elwispInfo(), elwisp: true}
}

func logInfo() command.Info {
	return command.Info{
		Name:        "log",
		Args:        "[options]",
		Description: "运行日志",
		Help: strings.TrimSpace(`-n, --limit <n>       条数，默认 5
--days <n>            天数，默认 1
--level <level>       最低 debug|info|warn|error，默认 debug
-d/-i/-w/-e           级别简写
--raw                原始记录
-u/-a                用户/助手消息
-s, --system         系统提示词
--hook               Hook 日志
--since/--until <time>  时间范围，如 2h、2026-06-03
--msg <text>          正文过滤
--contains <text>     正文及原始记录搜索`),
	}
}

func NewLog(deps Deps) command.Handler {
	return logCommand{deps: deps, info: logInfo()}
}

type logCommand struct {
	deps   Deps
	info   command.Info
	audit  bool
	elwisp bool
}

func (c logCommand) Info() command.Info { return c.info }

func (c logCommand) Handle(ctx context.Context, req command.Request) (*command.Result, error) {
	if wantsCommandHelp(req.Args) {
		return formatCommandHelp(c.deps.CommandText, c.info), nil
	}
	if c.audit {
		query, err := parseAuditQuery(req.Args)
		if err != nil {
			return nil, err
		}
		return queryLogs(ctx, c.deps, query, formatLogEntries(events.LogAudit, query.Raw))
	}
	if c.elwisp {
		query, err := parseElwispLogQuery(req.Args)
		if err != nil {
			return nil, err
		}
		return queryLogs(ctx, c.deps, query, formatLogEntries(events.LogElnis, query.Raw))
	}
	query, err := parseRuntimeLogQuery(req.Args)
	if err != nil {
		return nil, err
	}
	return queryLogs(ctx, c.deps, query, formatLogEntries(events.LogRuntime, query.Raw))
}

func (c logCommand) Complete(ctx context.Context, req command.CompletionRequest) []command.Completion {
	token := command.CurrentCompletionToken(req)
	previous := previousLogToken(req.Raw, token.Start)
	if c.audit && previous == "--run_source" {
		return completeStringOptions([]string{"foreground", "cron", "elnis"}, token.Text, token.Start, token.End, "run_source")
	}
	if previous == "--level" {
		return completeStringOptions([]string{"debug", "info", "warn", "error"}, token.Text, token.Start, token.End, "log_level")
	}
	if c.audit && previous == "--risk" {
		return completeStringOptions([]string{"low", "medium", "high", "critical"}, token.Text, token.Start, token.End, "risk")
	}
	if c.audit && previous == "--event" {
		return completeStringOptions([]string{"llm_usage", "llm_error", "session_naming_completed", "tool_call_prepared", "tool_call_completed", "permission_denied", "session_resume", "session_fork", "hook_tool_call_completed", "hook_tool_log"}, token.Text, token.Start, token.End, "audit_event")
	}
	if c.audit && previous == "--tool" && !strings.HasPrefix(token.Text, "-") {
		return completeToolNames(ctx, c.deps, token.Text, token.Start, token.End)
	}
	if c.elwisp && previous == "--mode" {
		return completeStringOptions([]string{"record", "direct", "llm"}, token.Text, token.Start, token.End, "elnis_mode")
	}
	if strings.HasPrefix(token.Text, "-") || token.Text == "" {
		return command.CompleteStaticOptions(logCompletionOptions(c.audit, c.elwisp), token.Text, token.Start, token.End, "option")
	}
	return nil
}

func logCompletionOptions(audit, elwisp bool) []command.CompletionOption {
	options := []command.CompletionOption{
		{Text: "-n", Description: "条数"},
		{Text: "--limit", Description: "条数"},
		{Text: "--days", Description: "天数"},
		{Text: "--level", Description: "最低级别"},
		{Text: "--raw", Description: "原始记录"},
		{Text: "-d", Description: "debug"},
		{Text: "-i", Description: "info"},
		{Text: "-w", Description: "warn"},
		{Text: "-e", Description: "error"},
		{Text: "--since", Description: "起始时间"},
		{Text: "--until", Description: "截止时间"},
		{Text: "--msg", Description: "正文过滤"},
		{Text: "--contains", Description: "文本搜索"},
	}
	if !elwisp {
		options = append(options,
			command.CompletionOption{Text: "--hook", Description: "Hook 日志"},
		)
	}
	if !audit && !elwisp {
		options = append(options,
			command.CompletionOption{Text: "-u", Description: "用户消息"},
			command.CompletionOption{Text: "-a", Description: "助手消息"},
			command.CompletionOption{Text: "-s", Description: "系统提示词"},
			command.CompletionOption{Text: "--system", Description: "系统提示词"},
		)
	}
	if audit {
		options = append(options,
			command.CompletionOption{Text: "-t", Description: "工具开始/结束"},
			command.CompletionOption{Text: "--run_source", Description: "来源: foreground/cron/elnis"},
			command.CompletionOption{Text: "--event", Description: "事件过滤"},
			command.CompletionOption{Text: "--risk", Description: "风险过滤"},
			command.CompletionOption{Text: "--actor", Description: "用户 ID"},
			command.CompletionOption{Text: "--session", Description: "会话 ID"},
			command.CompletionOption{Text: "--tool", Description: "工具名称"},
		)
	}
	if elwisp {
		options = append(options,
			command.CompletionOption{Text: "--name", Description: "Elwisp 名称"},
			command.CompletionOption{Text: "--elwisp", Description: "Elwisp 名称"},
			command.CompletionOption{Text: "--source", Description: "事件来源"},
			command.CompletionOption{Text: "--tag", Description: "Elwisp 标签"},
			command.CompletionOption{Text: "--id", Description: "来源事件 ID"},
			command.CompletionOption{Text: "--source-id", Description: "来源事件 ID"},
			command.CompletionOption{Text: "--mode", Description: "Elnis 模式"},
			command.CompletionOption{Text: "--event-key", Description: "事件 key"},
			command.CompletionOption{Text: "--event-id", Description: "内部事件 ID"},
			command.CompletionOption{Text: "--token", Description: "token 名"},
		)
	}
	return options
}

func previousLogToken(raw string, tokenStart int) string {
	before := strings.TrimSpace(raw[:tokenStart])
	if before == "" {
		return ""
	}
	fields := strings.Fields(before)
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

func queryLogs(ctx context.Context, deps Deps, query logging.LogQuery, formatter func([]logging.LogEntry) string) (*command.Result, error) {
	if deps.Logs == nil {
		return &command.Result{Content: "logs: not configured"}, nil
	}
	entries, err := deps.Logs.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	content := "nil"
	if len(entries.Entries) > 0 {
		content = formatter(entries.Entries)
	}
	if entries.DamagedCount > 0 {
		content += fmt.Sprintf("\nwarn: damaged=%d · results incomplete", entries.DamagedCount)
		if query.Raw {
			for _, raw := range entries.DamagedLines {
				content += "\n损坏记录（无法判断是否匹配筛选条件）：\n" + raw
			}
			if entries.DamagedCount > len(entries.DamagedLines) {
				content += fmt.Sprintf("\ndamaged hidden=%d", entries.DamagedCount-len(entries.DamagedLines))
			}
		}
	}
	return &command.Result{Content: content}, nil
}

type LogModule struct{}

func (LogModule) RegisterCommands(registrar Registrar, deps Deps) error {
	return RegisterFactories(registrar, deps,
		NewLog,
		NewAudit,
		NewElwisp,
		NewUsage,
	)
}
