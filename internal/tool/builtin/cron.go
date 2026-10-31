// 实现 Cron 工具及任务管理规则，只通过内部契约访问根部注入的能力。
package builtin

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/elyph"
	"elbot/internal/llm"
	toolshare "elbot/internal/tool/share"
	"encoding/json"
	"fmt"
	robfigcron "github.com/robfig/cron/v3"
	"slices"
	"strings"
	"time"
)

type CronTool struct {
	now func() time.Time
}
type CronQueryTool struct {
	service *cronService
}
type CronWriteTool struct {
	service *cronService
	now     func() time.Time
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
	ModelProvider       *string  `json:"model_provider"`
	Model               *string  `json:"model"`
	AllEnabledPlatforms *bool    `json:"all_enabled_platforms"`
	Enabled             *bool    `json:"enabled"`
}

func NewCronTools(repo CronRepository, validateModel func(provider, model string) error, now func() time.Time) []toolshare.Tool {
	service := &cronService{repo: repo, validateModel: validateModel, now: now}
	return []toolshare.Tool{CronTool{now: now}, CronQueryTool{service: service}, CronWriteTool{service: service, now: now}}
}

// cronService 只处理任务规则；读写和模型校验均通过根部注入的能力完成。
type cronService struct {
	repo          CronRepository
	validateModel func(provider, model string) error // 根部桥接的模型选择校验，不依赖模型服务类型。
	now           func() time.Time
}

func (s *cronService) CreateCron(ctx context.Context, req CronCreateRequest) (CronInfo, error) {
	if strings.TrimSpace(req.SourcePlatform) == "" {
		req.SourcePlatform = req.Actor.Platform
	}
	return s.upsert(ctx, req, true)
}

func (s *cronService) UpdateCron(ctx context.Context, patch CronUpdateRequest) (CronInfo, error) {
	info, err := s.repo.GetByName(ctx, patch.Name)
	if err != nil {
		return CronInfo{}, err
	}
	req := CronCreateRequest{
		Name: info.Name, Title: info.Title, ScheduleMode: info.ScheduleMode,
		RunAt: info.RunAt, CronExpr: info.CronExpr, TriggerMode: info.TriggerMode,
		Message: info.Message, ToolListNames: info.ToolListNames,
		ModelProvider: info.ModelProvider, Model: info.Model,
		AllEnabledPlatforms: info.AllEnabledPlatforms, Enabled: info.Enabled,
		Actor: info.CreatedBy, SourcePlatform: info.SourcePlatform,
	}
	before := req
	if patch.Title != nil {
		req.Title = strings.TrimSpace(*patch.Title)
	}
	if patch.ScheduleMode != nil {
		req.ScheduleMode = *patch.ScheduleMode
	}
	if patch.RunAt != nil {
		req.RunAt = strings.TrimSpace(*patch.RunAt)
	}
	if patch.CronExpr != nil {
		req.CronExpr = strings.TrimSpace(*patch.CronExpr)
	}
	if patch.TriggerMode != nil {
		req.TriggerMode = *patch.TriggerMode
	}
	if patch.Message != nil {
		req.Message = strings.TrimSpace(*patch.Message)
	}
	if patch.ToolListNames != nil {
		req.ToolListNames = normalizeCronToolNames(*patch.ToolListNames)
	}
	if patch.ModelProvider != nil || patch.Model != nil {
		if patch.ModelProvider == nil || patch.Model == nil {
			return CronInfo{}, fmt.Errorf("model_provider and model must be updated together")
		}
		req.ModelProvider, req.Model = strings.TrimSpace(*patch.ModelProvider), strings.TrimSpace(*patch.Model)
	}
	if patch.AllEnabledPlatforms != nil {
		req.AllEnabledPlatforms = *patch.AllEnabledPlatforms
	}
	if patch.Enabled != nil {
		req.Enabled = *patch.Enabled
	}
	reset := req.Enabled && ((patch.Enabled != nil && *patch.Enabled) ||
		before.ScheduleMode != req.ScheduleMode || before.RunAt != req.RunAt || before.CronExpr != req.CronExpr ||
		before.TriggerMode != req.TriggerMode || before.Message != req.Message ||
		before.AllEnabledPlatforms != req.AllEnabledPlatforms ||
		before.ModelProvider != req.ModelProvider || before.Model != req.Model ||
		!slices.Equal(before.ToolListNames, req.ToolListNames))
	return s.upsert(ctx, req, reset)
}

func (s *cronService) GetCron(ctx context.Context, name string, _ contextinfo.Actor) (CronInfo, error) {
	return s.repo.GetByName(ctx, name)
}

func (s *cronService) ListCrons(ctx context.Context, includeDisabled, includeCompleted bool, _ contextinfo.Actor) ([]CronInfo, error) {
	jobs, err := s.repo.List(ctx, includeDisabled)
	if err != nil {
		return nil, err
	}
	result := []CronInfo{}
	for _, job := range jobs {
		if includeCompleted || !job.Completed {
			result = append(result, job)
		}
	}
	return result, nil
}

func (s *cronService) DisableCron(ctx context.Context, name string, _ contextinfo.Actor) error {
	return s.repo.DisableByName(ctx, name)
}

func (s *cronService) DeleteCron(ctx context.Context, name string, _ contextinfo.Actor) error {
	return s.repo.DeleteByName(ctx, name)
}

func (s *cronService) upsert(ctx context.Context, req CronCreateRequest, reset bool) (CronInfo, error) {
	req.Name, req.Title = strings.TrimSpace(req.Name), strings.TrimSpace(req.Title)
	req.Message, req.SourcePlatform = strings.TrimSpace(req.Message), strings.TrimSpace(req.SourcePlatform)
	req.RunAt, req.CronExpr = strings.TrimSpace(req.RunAt), strings.TrimSpace(req.CronExpr)
	req.ModelProvider, req.Model = strings.TrimSpace(req.ModelProvider), strings.TrimSpace(req.Model)
	req.ToolListNames = normalizeCronToolNames(req.ToolListNames)
	if req.Name == "" {
		return CronInfo{}, fmt.Errorf("cron name is required")
	}
	if req.Title == "" {
		req.Title = req.Name
	}
	if req.Message == "" {
		return CronInfo{}, fmt.Errorf("message is required")
	}
	switch req.TriggerMode {
	case "direct":
	case "llm":
		if _, err := elyph.ParseTask(req.Message, ""); err != nil {
			return CronInfo{}, err
		}
	default:
		return CronInfo{}, fmt.Errorf("unsupported trigger mode %q", req.TriggerMode)
	}
	if req.SourcePlatform == "" {
		return CronInfo{}, fmt.Errorf("source platform is required")
	}
	if req.ModelProvider != "" || req.Model != "" {
		if req.ModelProvider == "" || req.Model == "" {
			return CronInfo{}, fmt.Errorf("model_provider and model must both be set or cleared")
		}
		if err := s.validateModel(req.ModelProvider, req.Model); err != nil {
			return CronInfo{}, err
		}
	}
	runAt := req.RunAt
	expr, next, err := cronSchedule(&req, s.now())
	if err != nil {
		return CronInfo{}, err
	}
	// 当前分钟的一次性计划会顺延；这同样改变了执行计划，需要开启新一轮。
	reset = reset || req.RunAt != runAt
	return s.repo.Upsert(ctx, CronSaveRequest{Definition: req, Schedule: expr, NextRunAt: next, ResetDelivery: reset})
}

func cronSchedule(req *CronCreateRequest, now time.Time) (string, *time.Time, error) {
	const timeLayout = "2006-01-02 15:04:05"
	var next *time.Time
	expr := req.CronExpr
	switch req.ScheduleMode {
	case "once":
		runAt, err := time.ParseInLocation(timeLayout, req.RunAt, time.Local)
		if err != nil {
			return "", nil, fmt.Errorf("run_at must use YYYY-MM-DD HH:MM:SS: %w", err)
		}
		if req.Enabled {
			nowMinute, runMinute := now.Truncate(time.Minute), runAt.Truncate(time.Minute)
			if runMinute.Before(nowMinute) {
				return "", nil, fmt.Errorf("run_at 时间不正确：一次性 cron 是分钟级调度，必须晚于当前这一分钟。当前时间：%s", now.Format(timeLayout))
			}
			if runMinute.Equal(nowMinute) {
				runAt = nowMinute.Add(time.Minute)
				req.RunAt = runAt.Format(timeLayout)
			}
			next = &runAt
		}
		expr = fmt.Sprintf("%d %d %d %d *", runAt.Minute(), runAt.Hour(), runAt.Day(), int(runAt.Month()))
	case "cron":
		if len(strings.Fields(expr)) != 5 {
			return "", nil, fmt.Errorf("cron_expr must be a 5-field cron expression")
		}
	default:
		return "", nil, fmt.Errorf("unsupported schedule mode %q", req.ScheduleMode)
	}
	parsed, err := robfigcron.ParseStandard(expr)
	if err != nil {
		return "", nil, err
	}
	if req.Enabled && req.ScheduleMode == "cron" {
		value := parsed.Next(now)
		if !value.IsZero() {
			next = &value
		}
	}
	return expr, next, nil
}

func normalizeCronToolNames(names []string) []string {
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" && !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	return result
}

func (t CronTool) Name() string { return "cron" }
func (t CronTool) Info() toolshare.Info {
	return toolshare.NewBuilder(t.Name()).Description("定时任务入口；查询→cron_query，修改→cron_write；调用本工具仅返回指引和当前时间。").Risk(toolshare.RiskMedium).SuperadminOnly().DependsOn("cron_query", "cron_write").BuildInfo()
}
func (t CronTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), t.Info().Description).BuildSchema()
}
func (t CronTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, t.Info().Description, func(ctx context.Context) (*toolshare.Result, string, error) {
		now := t.now().Format("2006-01-02 15:04:05")
		result := textResult("cron 是定时任务管理入口。当前本地时间：" + now + "。请调用 cron_query 或 cron_write。cron_query 不传 name 时列出 cron，传 name 时查询单个 cron。")
		return result, "cron guide returned; current_time=" + now, nil
	}), nil
}

// DiscoveryContent appends the current local time to the discover_tool text.
// The cron entry tool is the natural place to surface this because users often
// discover cron tools to set up time-based reminders.
func (t CronTool) DiscoveryContent() (string, bool) {
	return "当前本地时间：" + t.now().Format("2006-01-02 15:04:05"), false
}

func (t CronQueryTool) Name() string { return "cron_query" }
func (t CronQueryTool) Info() toolshare.Info {
	return hiddenCronInfo(t.Name(), "查定时任务；name空→全列表，否则→单个。", toolshare.RiskMedium)
}
func (t CronQueryTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), t.Info().Description).
		String("name", "任务短名。").
		Boolean("include_disabled", "列表包含停用项，默认false。").
		Boolean("include_completed", "列表包含已完成项，默认false。").
		BuildSchema()
}
func (t CronQueryTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args cronQueryArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, t.Info().Description, func(ctx context.Context) (*toolshare.Result, string, error) {
		if strings.TrimSpace(args.Name) != "" {
			view, err := t.service.GetCron(ctx, args.Name, actorFromContext(ctx))
			if err != nil {
				return nil, err.Error(), err
			}
			result, callErr := jsonResult(view)
			if result != nil {
				result.NeedsElyphRules = view.TriggerMode == "llm"
			}
			logContent := fmt.Sprintf("name=%s enabled=%t completed=%t", view.Name, view.Enabled, view.Completed)
			if callErr != nil {
				logContent = callErr.Error()
			}
			return result, logContent, callErr
		}
		views, err := t.service.ListCrons(ctx, args.IncludeDisabled, args.IncludeCompleted, actorFromContext(ctx))
		if err != nil {
			return nil, err.Error(), err
		}
		result, callErr := jsonResult(views)
		logContent := fmt.Sprintf("returned=%d", len(views))
		for _, view := range views {
			if result != nil && view.TriggerMode == "llm" {
				result.NeedsElyphRules = true
			}
			logContent += fmt.Sprintf("\nname=%s enabled=%t completed=%t", view.Name, view.Enabled, view.Completed)
		}
		if callErr != nil {
			logContent = callErr.Error()
		}
		return result, logContent, callErr
	}), nil
}

func (t CronWriteTool) Name() string { return "cron_write" }
func (t CronWriteTool) Info() toolshare.Info {
	info := hiddenCronInfo(t.Name(), "创建/更新/停用/永久删除定时任务。", toolshare.RiskHigh)
	info.NeedsElyphRules = true
	return info
}
func (t CronWriteTool) Schema() llm.ToolSchema {
	return cronBuilder(t.Name(), cronWriteDescription()).
		String("operation", "disable=停用保留；delete=永久删除；暂停用disable。", toolshare.Required(), toolshare.Enum("create", "update", "disable", "delete")).
		String("name", "任务短名：字母/数字/下划线/点/横线。", toolshare.Required()).
		String("title", "展示标题；create默认name。").
		String("schedule_mode", "once⇒run_at或非零run_after_*；cron⇒cron_expr。", toolshare.Enum("once", "cron")).
		String("run_at", "绝对时间YYYY-MM-DD HH:MM:SS。").
		String("cron_expr", "5字段：分 时 日 月 周。").
		Integer("run_after_minutes", "相对分钟数，按时长。").
		Integer("run_after_hours", "相对小时数，按时长。").
		Integer("run_after_days", "相对日历天数。").
		Integer("run_after_weeks", "相对周数，1周=7日历天。").
		Integer("run_after_months", "相对日历月数。").
		String("trigger_mode", "direct=直接通知；llm=后台LLM任务。", toolshare.Enum("direct", "llm")).
		String("message", "direct→通知正文；llm→ELyph #task <name> - 描述 任务文本。").
		StringArray("tool_list_names", "llm允许的工具/Skill/标签名，无需@；仅所选能力+必要依赖，无discover_tool；update: []→清空。").
		String("model_provider", "与model同传；create省略→work模型；update两者同为\"\"→默认模型。").
		String("model", "模型名，与model_provider成对。").
		Boolean("all_enabled_platforms", "true→广播到所有已启用平台的超级管理员；create默认false。").
		Boolean("enabled", "create默认true；false→停用保留。").
		BuildSchema()
}
func (t CronWriteTool) PrepareCall(ctx context.Context, req toolshare.CallRequest) (*toolshare.PreparedCall, error) {
	var args cronWriteArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return nil, err
	}
	return toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: t.Info().Risk}, fmt.Sprintf("操作：%s\n任务：%s", args.Operation, args.Name), func(ctx context.Context) (*toolshare.Result, string, error) {
		var result *toolshare.Result
		var callErr error
		switch strings.ToLower(strings.TrimSpace(args.Operation)) {
		case "create":
			result, callErr = t.create(ctx, args)
		case "update":
			result, callErr = t.update(ctx, args)
		case "disable":
			if err := t.service.DisableCron(ctx, args.Name, actorFromContext(ctx)); err != nil {
				return nil, err.Error(), err
			}
			result, callErr = jsonResult(map[string]any{"ok": true})
		case "delete":
			if err := t.service.DeleteCron(ctx, args.Name, actorFromContext(ctx)); err != nil {
				return nil, err.Error(), err
			}
			result, callErr = jsonResult(map[string]any{"ok": true})
		default:
			callErr = fmt.Errorf("operation must be create, update, disable, or delete")
		}
		logContent := ""
		if result != nil {
			logContent = result.Content
		}
		if callErr != nil {
			if logContent != "" {
				logContent += "\n"
			}
			logContent += callErr.Error()
		}
		return result, logContent, callErr
	}), nil
}

func (t CronWriteTool) create(ctx context.Context, args cronWriteArgs) (*toolshare.Result, error) {
	if (args.ModelProvider == nil) != (args.Model == nil) {
		return nil, fmt.Errorf("model_provider and model must be provided together")
	}
	provider, model := "", ""
	if args.ModelProvider != nil {
		provider, model = *args.ModelProvider, *args.Model
	}
	enabled := true
	if args.Enabled != nil {
		enabled = *args.Enabled
	}
	actor := actorFromContext(ctx)
	allEnabledPlatforms := false
	if args.AllEnabledPlatforms != nil {
		allEnabledPlatforms = *args.AllEnabledPlatforms
	}
	runAt, err := resolveRunAtAt(args.RunAt, relativeOffset{Minutes: args.RunAfterMinutes, Hours: args.RunAfterHours, Days: args.RunAfterDays, Weeks: args.RunAfterWeeks, Months: args.RunAfterMonths}, t.now())
	if err != nil {
		return nil, err
	}
	job, err := t.service.CreateCron(ctx, CronCreateRequest{Name: args.Name, Title: args.Title, ScheduleMode: args.ScheduleMode, RunAt: runAt, CronExpr: args.CronExpr, TriggerMode: args.TriggerMode, Message: args.Message, ToolListNames: args.ToolListNames, ModelProvider: provider, Model: model, AllEnabledPlatforms: allEnabledPlatforms, Enabled: enabled, Actor: actor, SourcePlatform: actor.Platform})
	if err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"ok": true, "job": job})
}

func (t CronWriteTool) update(ctx context.Context, args cronWriteArgs) (*toolshare.Result, error) {
	runAt, err := resolveUpdateRunAtAt(stringPtrIfNotEmpty(args.RunAt), relativeOffset{Minutes: args.RunAfterMinutes, Hours: args.RunAfterHours, Days: args.RunAfterDays, Weeks: args.RunAfterWeeks, Months: args.RunAfterMonths}, t.now())
	if err != nil {
		return nil, err
	}
	patch := CronUpdateRequest{Name: args.Name, Title: stringPtrIfNotEmpty(args.Title), RunAt: runAt, CronExpr: stringPtrIfNotEmpty(args.CronExpr), Message: stringPtrIfNotEmpty(args.Message), ModelProvider: args.ModelProvider, Model: args.Model, AllEnabledPlatforms: args.AllEnabledPlatforms, Enabled: args.Enabled, Actor: actorFromContext(ctx)}
	if args.ScheduleMode != "" {
		v := args.ScheduleMode
		patch.ScheduleMode = &v
	}
	if args.TriggerMode != "" {
		v := args.TriggerMode
		patch.TriggerMode = &v
	}
	if args.ToolListNames != nil {
		patch.ToolListNames = &args.ToolListNames
	}
	job, err := t.service.UpdateCron(ctx, patch)
	if err != nil {
		return nil, err
	}
	return jsonResult(map[string]any{"ok": true, "job": job})
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
	return `写定时任务。
create⇒schedule_mode,trigger_mode,message必填。
update：省略→保持原值；普通字符串""→保持原值(model_provider/model除外)；false有效。
时间=服务器local，分钟级；run_at与非零run_after_*至多选一。
run_after_*≥0，可组合；用户给相对时间→优先对应字段，不自行换算run_at。
once且enabled：早于当前分钟→error；当前分钟→下一分钟。`
}

func stringPtrIfNotEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func hiddenCronInfo(name, description string, risk toolshare.RiskLevel) toolshare.Info {
	return cronBuilder(name, description).Risk(risk).Hidden().SuperadminOnly().BuildInfo()
}

func cronBuilder(name, description string) *toolshare.Builder {
	return toolshare.NewBuilder(name).Description(description).Risk(toolshare.RiskMedium).SuperadminOnly()
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

func actorFromContext(ctx context.Context) contextinfo.Actor {
	actor, ok := contextinfo.ActorFromContext(ctx)
	if !ok {
		return contextinfo.Actor{Role: contextinfo.RoleUser}
	}
	return actor
}

func jsonResult(value any) (*toolshare.Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return &toolshare.Result{Content: string(data)}, nil
}

func textResult(text string) *toolshare.Result {
	text = strings.TrimSpace(text)
	return &toolshare.Result{Content: text}
}
