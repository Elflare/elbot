package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	elcron "elbot/internal/cron"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
	"elbot/internal/tool/runtimeinfo"
)

type CronTool struct {
	service *elcron.Service
	info    runtimeinfo.Info
}
type CronQueryTool struct {
	service *elcron.Service
	info    runtimeinfo.Info
}
type CronWriteTool struct {
	service *elcron.Service
	info    runtimeinfo.Info
}

type cronCreateArgs struct {
	Name                string   `json:"name"`
	Title               string   `json:"title"`
	ScheduleMode        string   `json:"schedule_mode"`
	RunAt               string   `json:"run_at"`
	CronExpr            string   `json:"cron_expr"`
	RunAfterMinutes     int      `json:"run_after_minutes"`
	RunAfterHours       int      `json:"run_after_hours"`
	RunAfterDays        int      `json:"run_after_days"`
	RunAfterWeeks       int      `json:"run_after_weeks"`
	RunAfterMonths      int      `json:"run_after_months"`
	TriggerMode         string   `json:"trigger_mode"`
	Message             string   `json:"message"`
	ToolListNames       []string `json:"tool_list_names"`
	SessionMode         string   `json:"session_mode"`
	AllEnabledPlatforms bool     `json:"all_enabled_platforms"`
	Enabled             *bool    `json:"enabled"`
}

type cronQueryArgs struct {
	Name             string `json:"name"`
	IncludeDisabled  bool   `json:"include_disabled"`
	IncludeCompleted bool   `json:"include_completed"`
}

type cronWriteArgs struct {
	Operation           string   `json:"operation"`
	Name                string   `json:"name"`
	Title               string   `json:"title"`
	ScheduleMode        string   `json:"schedule_mode"`
	RunAt               string   `json:"run_at"`
	CronExpr            string   `json:"cron_expr"`
	RunAfterMinutes     int      `json:"run_after_minutes"`
	RunAfterHours       int      `json:"run_after_hours"`
	RunAfterDays        int      `json:"run_after_days"`
	RunAfterWeeks       int      `json:"run_after_weeks"`
	RunAfterMonths      int      `json:"run_after_months"`
	TriggerMode         string   `json:"trigger_mode"`
	Message             string   `json:"message"`
	ToolListNames       []string `json:"tool_list_names"`
	SessionMode         string   `json:"session_mode"`
	AllEnabledPlatforms *bool    `json:"all_enabled_platforms"`
	Enabled             *bool    `json:"enabled"`
}

func NewCronTools(service *elcron.Service, infos ...runtimeinfo.Info) []tool.Tool {
	info := runtimeinfo.First(infos...)
	return []tool.Tool{CronTool{service: service, info: info}, CronQueryTool{service: service, info: info}, CronWriteTool{service: service, info: info}}
}

func (t CronTool) Name() string { return "cron" }
func (t CronTool) Info() tool.Info {
	return tool.NewBuilder(t.Name()).Description("管理系统 Cron 定时任务。本工具仅为入口").Risk(tool.RiskMedium).SuperadminOnly().DependsOn("cron_query", "cron_write").BuildInfo()
}
func (t CronTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), t.Info().Description).BuildSchema()
}
func (t CronTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	now := t.info.CurrentTime().Format("2006-01-02 15:04:05")
	return textResult("cron 是定时任务管理入口。当前本地时间：" + now + "。请调用 cron_query 或 cron_write。cron_query 不传 name 时列出 cron，传 name 时查询单个 cron。"), nil
}

// DiscoveryContent appends the current local time to the discover_tool text.
// The cron entry tool is the natural place to surface this because users often
// discover cron tools to set up time-based reminders.
func (t CronTool) DiscoveryContent() (string, bool) {
	return "当前本地时间：" + t.info.CurrentTime().Format("2006-01-02 15:04:05"), false
}

func (t CronQueryTool) Name() string { return "cron_query" }
func (t CronQueryTool) Info() tool.Info {
	return hiddenCronInfo(t.Name(), "查询单个 cron 或列出 cron。不传 name 时列出；传 name 时查询单个。", tool.RiskMedium)
}
func (t CronQueryTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), "查询单个 cron 或列出 cron。 ").
		String("name", "任务短名。留空则列出 cron。").
		Boolean("include_disabled", "列出 cron 时是否包含已停用 cron。默认 false。 ").
		Boolean("include_completed", "列出 cron 时是否显示已完成的 cron。默认 false；想查看历史一次性任务时设为 true。 ").
		BuildSchema()
}
func (t CronQueryTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args cronQueryArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Name) != "" {
		view, err := t.service.Get(ctx, args.Name, actorFromContext(ctx))
		if err != nil {
			return nil, err
		}
		return jsonResult(view)
	}
	views, err := t.service.List(ctx, args.IncludeDisabled, args.IncludeCompleted, actorFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return jsonResult(views)
}

func (t CronWriteTool) Name() string { return "cron_write" }
func (t CronWriteTool) Info() tool.Info {
	return hiddenCronInfo(t.Name(), "创建、Patch 更新、停用或硬删除 cron。operation 为 create、update、disable 或 delete。", tool.RiskHigh)
}
func (t CronWriteTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), cronWriteDescription()).
		String("operation", "写操作：create、update、disable、delete。", tool.Required()).
		String("name", "任务短名，只允许字母、数字、下划线、点和横线。", tool.Required()).
		String("title", "create 需要；update 可选。cron session 标题。为空时使用 name。").
		String("schedule_mode", "create 需要；update 可选。触发计划类型：once 或 cron。 ").
		String("run_at", "一次性任务绝对时间，格式 YYYY-MM-DD HH:MM:SS。schedule_mode=once 且未使用 run_after_* 时填写。\n~ 任意 run_after_* 同时传").
		String("cron_expr", "5 字段 cron 表达式。schedule_mode=cron 时填写。 ").
		Integer("run_after_minutes", "一次性任务相对当前时间的分钟偏移。\n** 用户说几分钟后时优先使用\n~ run_at 同时传").
		Integer("run_after_hours", "一次性任务相对当前时间的小时偏移。\n** 用户说几小时后时优先使用\n~ run_at 同时传").
		Integer("run_after_days", "一次性任务相对当前时间的天数偏移。\n** 用户说几天后时优先使用\n~ run_at 同时传").
		Integer("run_after_weeks", "一次性任务相对当前时间的周数偏移，1周按7天。\n** 用户说几周后时优先使用\n~ run_at 同时传").
		Integer("run_after_months", "一次性任务相对当前时间的日历月偏移。\n** 用户说几个月后时优先使用\n~ run_at 同时传").
		String("trigger_mode", "create 需要；update 可选。触发模式：direct 或 llm。direct 直接发消息；llm 后台运行 LLM 处理复杂任务。 ").
		String("message", "create 需要；update 可选。trigger_mode=direct：使用普通自然语言通知文本。trigger_mode=llm：使用 ELyph #task <name> - 描述 任务文本。").
		StringArray("tool_list_names", "trigger_mode=llm 时预注入的工具名或 Skill 名列表；普通工具会注入 schema，Skill 会注入任务说明并自动注入对应 runner。update 传空数组表示清空。 ").
		String("session_mode", "trigger_mode=llm 时后台 Session 模式：work 或 chat；默认 work。不需要工具时选chat").
		Boolean("all_enabled_platforms", "是否发送/广播到所有 enabled 平台超级管理员。 ").
		Boolean("enabled", "create：创建后是否启用，默认 true；update：是否启用。false 表示停用但保留记录。 ").
		BuildSchema()
}
func (t CronWriteTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args cronWriteArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimSpace(args.Operation)) {
	case "create":
		return t.create(ctx, args)
	case "update":
		return t.update(ctx, args)
	case "disable":
		if err := t.service.Disable(ctx, args.Name, actorFromContext(ctx)); err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"ok": true})
	case "delete":
		if err := t.service.Delete(ctx, args.Name, actorFromContext(ctx)); err != nil {
			return nil, err
		}
		return jsonResult(map[string]any{"ok": true})
	default:
		return nil, fmt.Errorf("operation must be create, update, disable, or delete")
	}
}

func (t CronWriteTool) create(ctx context.Context, args cronWriteArgs) (*tool.Result, error) {
	enabled := true
	if args.Enabled != nil {
		enabled = *args.Enabled
	}
	actor := actorFromContext(ctx)
	allEnabledPlatforms := false
	if args.AllEnabledPlatforms != nil {
		allEnabledPlatforms = *args.AllEnabledPlatforms
	}
	createArgs := cronCreateArgs{Name: args.Name, Title: args.Title, ScheduleMode: args.ScheduleMode, RunAt: args.RunAt, CronExpr: args.CronExpr, RunAfterMinutes: args.RunAfterMinutes, RunAfterHours: args.RunAfterHours, RunAfterDays: args.RunAfterDays, RunAfterWeeks: args.RunAfterWeeks, RunAfterMonths: args.RunAfterMonths, TriggerMode: args.TriggerMode, Message: args.Message, ToolListNames: args.ToolListNames, SessionMode: args.SessionMode, AllEnabledPlatforms: allEnabledPlatforms, Enabled: args.Enabled}
	runAt, err := resolveCreateRunAtAt(createArgs, t.info.CurrentTime())
	if err != nil {
		return nil, err
	}
	job, err := t.service.Create(ctx, elcron.UpsertRequest{Name: args.Name, Title: args.Title, ScheduleMode: elcron.ScheduleMode(args.ScheduleMode), RunAt: runAt, CronExpr: args.CronExpr, TriggerMode: elcron.TriggerMode(args.TriggerMode), Message: args.Message, ToolListNames: args.ToolListNames, SessionMode: args.SessionMode, AllEnabledPlatforms: allEnabledPlatforms, Enabled: enabled, Actor: actor, SourcePlatform: actor.Platform})
	if err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"ok": true, "job": job})
}

func (t CronWriteTool) update(ctx context.Context, args cronWriteArgs) (*tool.Result, error) {
	runAt, err := resolveUpdateRunAtAt(stringPtrIfNotEmpty(args.RunAt), relativeOffset{Minutes: args.RunAfterMinutes, Hours: args.RunAfterHours, Days: args.RunAfterDays, Weeks: args.RunAfterWeeks, Months: args.RunAfterMonths}, t.info.CurrentTime())
	if err != nil {
		return nil, err
	}
	patch := elcron.PatchRequest{Name: args.Name, Title: stringPtrIfNotEmpty(args.Title), RunAt: runAt, CronExpr: stringPtrIfNotEmpty(args.CronExpr), Message: stringPtrIfNotEmpty(args.Message), SessionMode: stringPtrIfNotEmpty(args.SessionMode), AllEnabledPlatforms: args.AllEnabledPlatforms, Enabled: args.Enabled, Actor: actorFromContext(ctx)}
	if args.ScheduleMode != "" {
		v := elcron.ScheduleMode(args.ScheduleMode)
		patch.ScheduleMode = &v
	}
	if args.TriggerMode != "" {
		v := elcron.TriggerMode(args.TriggerMode)
		patch.TriggerMode = &v
	}
	if args.ToolListNames != nil {
		patch.ToolListNames = &args.ToolListNames
	}
	job, err := t.service.Update(ctx, patch)
	if err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"ok": true, "job": job})
}

func resolveCreateRunAt(args cronCreateArgs) (string, error) {
	return resolveCreateRunAtAt(args, time.Now())
}

func resolveCreateRunAtAt(args cronCreateArgs, now time.Time) (string, error) {
	return resolveRunAtAt(args.RunAt, relativeOffset{Minutes: args.RunAfterMinutes, Hours: args.RunAfterHours, Days: args.RunAfterDays, Weeks: args.RunAfterWeeks, Months: args.RunAfterMonths}, now)
}

func resolveUpdateRunAt(runAt *string, offset relativeOffset) (*string, error) {
	return resolveUpdateRunAtAt(runAt, offset, time.Now())
}

func resolveUpdateRunAtAt(runAt *string, offset relativeOffset, now time.Time) (*string, error) {
	if !offset.hasAny() {
		return runAt, nil
	}
	value := ""
	if runAt != nil {
		value = *runAt
	}
	resolved, err := resolveRunAtAt(value, offset, now)
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}

type relativeOffset struct {
	Minutes int
	Hours   int
	Days    int
	Weeks   int
	Months  int
}

func (o relativeOffset) hasAny() bool {
	return o.Minutes != 0 || o.Hours != 0 || o.Days != 0 || o.Weeks != 0 || o.Months != 0
}

func resolveRunAt(runAt string, offset relativeOffset) (string, error) {
	return resolveRunAtAt(runAt, offset, time.Now())
}

func resolveRunAtAt(runAt string, offset relativeOffset, now time.Time) (string, error) {
	runAt = strings.TrimSpace(runAt)
	if !offset.hasAny() {
		return runAt, nil
	}
	if runAt != "" {
		return "", fmt.Errorf("run_at 不能和 run_after_* 同时传；请只使用绝对时间或相对偏移之一")
	}
	if offset.Minutes < 0 || offset.Hours < 0 || offset.Days < 0 || offset.Weeks < 0 || offset.Months < 0 {
		return "", fmt.Errorf("run_after_* 必须是正数；当前本地时间：%s", now.Format("2006-01-02 15:04:05"))
	}
	if offset.Months == 0 && offset.Weeks == 0 && offset.Days == 0 && offset.Hours == 0 && offset.Minutes == 0 {
		return "", fmt.Errorf("run_after_* 至少需要一个正数；当前本地时间：%s", now.Format("2006-01-02 15:04:05"))
	}
	run := now.AddDate(0, offset.Months, offset.Weeks*7+offset.Days).Add(time.Duration(offset.Hours)*time.Hour + time.Duration(offset.Minutes)*time.Minute)
	return run.Format("2006-01-02 15:04:05"), nil
}

func cronWriteDescription() string {
	return "创建、Patch 更新、停用或硬删除 cron。\n\n" + runtimeinfo.ElyphRuleCard() + `

#task cron_write_desc - cron_write 总规则
** operation=create 时创建一次性或周期 cron；title、schedule_mode、trigger_mode、message 通常需要填写
** operation=update 时只修改传入字段
** operation=disable 时停用 cron 但保留记录
** operation=delete 时硬删除 cron，不保留调度记录；用户只想暂停时优先用 disable
** trigger_mode=direct 时，message 使用普通自然语言通知文本
** trigger_mode=llm 时，message 使用 ELyph #task <name> - 描述 任务文本
** 用户说几分钟/几小时/几天/几周/几个月后时，优先使用对应 run_after_*
** create 时 enabled 缺省为 true
~ 自己计算用户相对时间对应的 run_at
~ 同时传 run_at 和任意 run_after_*`
}

func stringPtrIfNotEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func hiddenCronInfo(name, description string, risk tool.RiskLevel) tool.Info {
	return cronBuilder(name, description).Risk(risk).Hidden().SuperadminOnly().BuildInfo()
}

func cronBuilder(name, description string) *tool.Builder {
	return tool.NewBuilder(name).Description(description).Risk(tool.RiskMedium).SuperadminOnly()
}

func decodeArgs(raw json.RawMessage, out any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("parse cron arguments: %w", err)
	}
	return nil
}

func actorFromContext(ctx context.Context) security.Actor {
	actor, ok := security.ActorFromContext(ctx)
	if !ok {
		return security.Actor{Role: security.RoleUser}
	}
	return actor
}

func jsonResult(value any) (*tool.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &tool.Result{Content: string(data)}, nil
}

func textResult(text string) *tool.Result {
	text = strings.TrimSpace(text)
	return &tool.Result{Content: text}
}
