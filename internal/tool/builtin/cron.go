package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	elcron "elbot/internal/cron"
	"elbot/internal/elyph"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/tool"
)

type CronTool struct{ service *elcron.Service }
type CronCreateTool struct{ service *elcron.Service }
type CronUpdateTool struct{ service *elcron.Service }
type CronDeleteTool struct{ service *elcron.Service }
type CronDisableTool struct{ service *elcron.Service }
type CronGetTool struct{ service *elcron.Service }
type CronListTool struct{ service *elcron.Service }

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
	AllEnabledPlatforms bool     `json:"all_enabled_platforms"`
	Enabled             *bool    `json:"enabled"`
}

type cronUpdateArgs struct {
	Name                string    `json:"name"`
	Title               *string   `json:"title"`
	ScheduleMode        *string   `json:"schedule_mode"`
	RunAt               *string   `json:"run_at"`
	CronExpr            *string   `json:"cron_expr"`
	RunAfterMinutes     *int      `json:"run_after_minutes"`
	RunAfterHours       *int      `json:"run_after_hours"`
	RunAfterDays        *int      `json:"run_after_days"`
	RunAfterWeeks       *int      `json:"run_after_weeks"`
	RunAfterMonths      *int      `json:"run_after_months"`
	TriggerMode         *string   `json:"trigger_mode"`
	Message             *string   `json:"message"`
	ToolListNames       *[]string `json:"tool_list_names"`
	AllEnabledPlatforms *bool     `json:"all_enabled_platforms"`
	Enabled             *bool     `json:"enabled"`
}

type cronNameArgs struct {
	Name string `json:"name"`
}
type cronListArgs struct {
	IncludeDisabled  bool `json:"include_disabled"`
	IncludeCompleted bool `json:"include_completed"`
}

func NewCronTools(service *elcron.Service) []tool.Tool {
	return []tool.Tool{CronTool{service}, CronCreateTool{service}, CronUpdateTool{service}, CronDeleteTool{service}, CronDisableTool{service}, CronGetTool{service}, CronListTool{service}}
}

func (t CronTool) Name() string { return "cron" }
func (t CronTool) Info() tool.Info {
	return tool.NewBuilder(t.Name()).Description("管理系统 Cron 定时任务。本工具仅为入口").Risk(tool.RiskMedium).SuperadminOnly().DependsOn("cron_create", "cron_update", "cron_delete", "cron_disable", "cron_get", "cron_list").BuildInfo()
}
func (t CronTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), t.Info().Description).BuildSchema()
}
func (t CronTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	now := time.Now().Format("2006-01-02 15:04:05")
	return textResult("cron 是定时任务管理入口。当前本地时间：" + now + "。请调用 cron_create、cron_update、cron_delete、cron_disable、cron_get 或 cron_list。"), nil
}

func (t CronCreateTool) Name() string { return "cron_create" }
func (t CronCreateTool) Info() tool.Info {
	return hiddenCronInfo(t.Name(), "创建一次性或周期 cron。", tool.RiskHigh)
}
func (t CronCreateTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), cronCreateDescription()).
		String("name", "任务短名，只允许字母、数字、下划线、点和横线。", tool.Required()).
		String("title", "cron session 标题。为空时使用 name。", tool.Required()).
		String("schedule_mode", "触发计划类型：once 或 cron。", tool.Required()).
		String("run_at", "一次性任务绝对时间，格式 YYYY-MM-DD HH:MM:SS。schedule_mode=once 且未使用 run_after_* 时填写。\n~ 任意 run_after_* 同时传").
		String("cron_expr", "5 字段 cron 表达式。schedule_mode=cron 时填写。").
		Integer("run_after_minutes", "一次性任务相对当前时间的分钟偏移。\n** 用户说几分钟后时优先使用\n~ run_at 同时传").
		Integer("run_after_hours", "一次性任务相对当前时间的小时偏移。\n** 用户说几小时后时优先使用\n~ run_at 同时传").
		Integer("run_after_days", "一次性任务相对当前时间的天数偏移。\n** 用户说几天后时优先使用\n~ run_at 同时传").
		Integer("run_after_weeks", "一次性任务相对当前时间的周数偏移，1周按7天。\n** 用户说几周后时优先使用\n~ run_at 同时传").
		Integer("run_after_months", "一次性任务相对当前时间的日历月偏移。\n** 用户说几个月后时优先使用\n~ run_at 同时传").
		String("trigger_mode", "触发模式：direct 或 llm。direct 直接发消息；llm 后台运行 LLM 处理复杂任务。", tool.Required()).
		String("message", "trigger_mode=direct：使用普通自然语言通知文本。\ntrigger_mode=llm：使用 ELyph #task <name> - 描述 任务文本。", tool.Required()).
		StringArray("tool_list_names", "trigger_mode=llm 时预注入的工具名列表；只传工具名。 ").
		Boolean("all_enabled_platforms", "是否发送给所有平台超级管理员。 ").
		Boolean("enabled", "创建后是否启用，默认 true。 ").
		BuildSchema()
}
func (t CronCreateTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args cronCreateArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	enabled := true
	if args.Enabled != nil {
		enabled = *args.Enabled
	}
	actor := actorFromContext(ctx)
	runAt, err := resolveCreateRunAt(args)
	if err != nil {
		return nil, err
	}
	job, err := t.service.Create(ctx, elcron.UpsertRequest{Name: args.Name, Title: args.Title, ScheduleMode: elcron.ScheduleMode(args.ScheduleMode), RunAt: runAt, CronExpr: args.CronExpr, TriggerMode: elcron.TriggerMode(args.TriggerMode), Message: args.Message, ToolListNames: args.ToolListNames, AllEnabledPlatforms: args.AllEnabledPlatforms, Enabled: enabled, Actor: actor, SourcePlatform: actor.Platform})

	if err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"ok": true, "job": job})
}

func (t CronUpdateTool) Name() string { return "cron_update" }
func (t CronUpdateTool) Info() tool.Info {
	return hiddenCronInfo(t.Name(), "Patch 更新 cron。", tool.RiskHigh)
}
func (t CronUpdateTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), cronUpdateDescription()).
		String("name", "任务短名。", tool.Required()).
		String("title", "新标题。 ").
		String("schedule_mode", "触发计划类型：once 或 cron。 ").
		String("run_at", "一次性任务绝对时间，格式 YYYY-MM-DD HH:MM:SS。\n~ 任意 run_after_* 同时传").
		String("cron_expr", "5 字段 cron 表达式。schedule_mode=cron 时填写。 ").
		Integer("run_after_minutes", "一次性任务相对当前时间的分钟偏移。\n** 用户说几分钟后时优先使用\n~ run_at 同时传").
		Integer("run_after_hours", "一次性任务相对当前时间的小时偏移。\n** 用户说几小时后时优先使用\n~ run_at 同时传").
		Integer("run_after_days", "一次性任务相对当前时间的天数偏移。\n** 用户说几天后时优先使用\n~ run_at 同时传").
		Integer("run_after_weeks", "一次性任务相对当前时间的周数偏移，1周按7天。\n** 用户说几周后时优先使用\n~ run_at 同时传").
		Integer("run_after_months", "一次性任务相对当前时间的日历月偏移。\n** 用户说几个月后时优先使用\n~ run_at 同时传").
		String("trigger_mode", "触发模式：direct 或 llm。direct 直接发消息；llm 后台运行 LLM 处理复杂任务。 ").
		String("message", "trigger_mode=direct：使用普通自然语言通知文本。\ntrigger_mode=llm：使用 ELyph #task <name> - 描述 任务文本。").
		StringArray("tool_list_names", "替换预注入工具名列表；只传工具名。传空数组表示清空。 ").
		Boolean("all_enabled_platforms", "是否广播到所有 enabled 平台超级管理员。 ").
		Boolean("enabled", "是否启用。false 表示停用但保留记录。 ").
		BuildSchema()
}
func (t CronUpdateTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args cronUpdateArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	runAt, err := resolveUpdateRunAt(args.RunAt, relativeOffset{Minutes: intValue(args.RunAfterMinutes), Hours: intValue(args.RunAfterHours), Days: intValue(args.RunAfterDays), Weeks: intValue(args.RunAfterWeeks), Months: intValue(args.RunAfterMonths)})
	if err != nil {
		return nil, err
	}
	patch := elcron.PatchRequest{Name: args.Name, Title: args.Title, RunAt: runAt, CronExpr: args.CronExpr, Message: args.Message, ToolListNames: args.ToolListNames, AllEnabledPlatforms: args.AllEnabledPlatforms, Enabled: args.Enabled, Actor: actorFromContext(ctx)}

	if args.ScheduleMode != nil {
		v := elcron.ScheduleMode(*args.ScheduleMode)
		patch.ScheduleMode = &v
	}
	if args.TriggerMode != nil {
		v := elcron.TriggerMode(*args.TriggerMode)
		patch.TriggerMode = &v
	}
	job, err := t.service.Update(ctx, patch)
	if err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"ok": true, "job": job})
}

func (t CronDeleteTool) Name() string { return "cron_delete" }
func (t CronDeleteTool) Info() tool.Info {
	return hiddenCronInfo(t.Name(), "硬删除 cron。", tool.RiskHigh)
}
func (t CronDeleteTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), "硬删除 cron，不保留调度记录。用户只想暂停时优先用 cron_disable。 ").String("name", "任务短名", tool.Required()).BuildSchema()
}
func (t CronDeleteTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args cronNameArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	if err := t.service.Delete(ctx, args.Name, actorFromContext(ctx)); err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"ok": true})
}

func (t CronDisableTool) Name() string { return "cron_disable" }
func (t CronDisableTool) Info() tool.Info {
	return hiddenCronInfo(t.Name(), "停用 cron 但保留记录。", tool.RiskHigh)
}
func (t CronDisableTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), "停用 cron 但保留记录").String("name", "任务短名", tool.Required()).BuildSchema()
}
func (t CronDisableTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args cronNameArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	if err := t.service.Disable(ctx, args.Name, actorFromContext(ctx)); err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"ok": true})
}

func (t CronGetTool) Name() string { return "cron_get" }
func (t CronGetTool) Info() tool.Info {
	return hiddenCronInfo(t.Name(), "查询单个 cron。", tool.RiskMedium)
}
func (t CronGetTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), "查询单个 cron。 ").String("name", "任务短名。", tool.Required()).BuildSchema()
}
func (t CronGetTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args cronNameArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	view, err := t.service.Get(ctx, args.Name, actorFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return jsonResult(view)
}

func (t CronListTool) Name() string { return "cron_list" }
func (t CronListTool) Info() tool.Info {
	return hiddenCronInfo(t.Name(), "列出 cron。", tool.RiskMedium)
}
func (t CronListTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), "列出 cron。 ").Boolean("include_disabled", "是否包含已停用 cron。默认 false。 ").Boolean("include_completed", "是否显示已完成的 cron。默认 false；想查看历史一次性任务时设为 true。 ").BuildSchema()
}
func (t CronListTool) Call(ctx context.Context, req tool.CallRequest) (*tool.Result, error) {
	var args cronListArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	views, err := t.service.List(ctx, args.IncludeDisabled, args.IncludeCompleted, actorFromContext(ctx))
	if err != nil {
		return nil, err
	}
	return jsonResult(views)
}

func cronCreateDescription() string {
	return "创建一次性或周期 cron。\n\n" + elyph.RuleCard() + `

#task cron_create_desc - cron_create 总规则
** trigger_mode=direct 时，message 使用普通自然语言通知文本
** trigger_mode=llm 时，message 使用 ELyph #task <name> - 描述 任务文本
** 用户说几分钟/几小时/几天/几周/几个月后时，优先使用对应 run_after_*
** enabled 缺省为 true
~ 自己计算用户相对时间对应的 run_at
~ 同时传 run_at 和任意 run_after_*`
}

func cronUpdateDescription() string {
	return "Patch 更新 cron。\n\n" + elyph.RuleCard() + `

#task cron_update_desc - cron_update 总规则
** 只修改传入字段
** trigger_mode=direct 时，message 使用普通自然语言通知文本
** trigger_mode=llm 时，message 使用 ELyph #task <name> - 描述 任务文本
** 用户说几分钟/几小时/几天/几周/几个月后时，优先使用对应 run_after_*
~ 自己计算用户相对时间对应的 run_at
~ 同时传 run_at 和任意 run_after_*`
}

func resolveCreateRunAt(args cronCreateArgs) (string, error) {
	return resolveRunAt(args.RunAt, relativeOffset{Minutes: args.RunAfterMinutes, Hours: args.RunAfterHours, Days: args.RunAfterDays, Weeks: args.RunAfterWeeks, Months: args.RunAfterMonths})
}

func resolveUpdateRunAt(runAt *string, offset relativeOffset) (*string, error) {
	if !offset.hasAny() {
		return runAt, nil
	}
	value := ""
	if runAt != nil {
		value = *runAt
	}
	resolved, err := resolveRunAt(value, offset)
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
	runAt = strings.TrimSpace(runAt)
	if !offset.hasAny() {
		return runAt, nil
	}
	if runAt != "" {
		return "", fmt.Errorf("run_at 不能和 run_after_* 同时传；请只使用绝对时间或相对偏移之一")
	}
	if offset.Minutes < 0 || offset.Hours < 0 || offset.Days < 0 || offset.Weeks < 0 || offset.Months < 0 {
		return "", fmt.Errorf("run_after_* 必须是正数；当前本地时间：%s", time.Now().Format("2006-01-02 15:04:05"))
	}
	if offset.Months == 0 && offset.Weeks == 0 && offset.Days == 0 && offset.Hours == 0 && offset.Minutes == 0 {
		return "", fmt.Errorf("run_after_* 至少需要一个正数；当前本地时间：%s", time.Now().Format("2006-01-02 15:04:05"))
	}
	now := time.Now()
	run := now.AddDate(0, offset.Months, offset.Weeks*7+offset.Days).Add(time.Duration(offset.Hours)*time.Hour + time.Duration(offset.Minutes)*time.Minute)
	return run.Format("2006-01-02 15:04:05"), nil
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
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
