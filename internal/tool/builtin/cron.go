// 实现 Cron 工具及任务管理规则，只通过内部契约访问根部注入的能力。
package builtin

import (
	"context"
	"elbot/internal/contextinfo"
	"elbot/internal/crondef"
	"elbot/internal/elyph"
	"elbot/internal/operationresult"
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
	TargetID            *string  `json:"target_id"`
	AllEnabledPlatforms *bool    `json:"all_enabled_platforms"`
	Enabled             *bool    `json:"enabled"`
}

func NewCronTools(repo CronRepository, validateModel func(provider, model string) operationresult.Result[struct{}], now func() time.Time) []toolshare.ToolDefinition {
	service := &cronService{repo: repo, validateModel: validateModel, now: now}
	return []toolshare.ToolDefinition{(CronTool{now: now}).Definition(), (CronQueryTool{service: service}).Definition(), (CronWriteTool{service: service, now: now}).Definition()}
}

// cronService 只处理任务规则；读写和模型校验均通过根部注入的能力完成。
type cronService struct {
	repo          CronRepository
	validateModel func(provider, model string) operationresult.Result[struct{}] // 根部桥接的模型选择校验，不依赖模型服务类型。
	now           func() time.Time
}

func (s *cronService) prepareUpdate(ctx context.Context, patch CronUpdateRequest) (operationresult.Result[CronSaveRequest], error) {
	result, err := s.repo.GetByName(ctx, patch.Name)
	if err != nil {
		return operationresult.Result[CronSaveRequest]{}, err
	}
	if result.Status != operationresult.Success {
		return operationresult.Result[CronSaveRequest]{Status: result.Status, Message: result.Message}, nil
	}
	info := result.Data
	req := CronCreateRequest{
		Name: info.Name, Title: info.Title, ScheduleMode: info.ScheduleMode,
		RunAt: info.RunAt, CronExpr: info.CronExpr, TriggerMode: info.TriggerMode,
		Message: info.Message, ToolListNames: info.ToolListNames,
		ModelProvider: info.ModelProvider, Model: info.Model,
		TargetID: info.TargetID, AllEnabledPlatforms: info.AllEnabledPlatforms, Enabled: info.Enabled,
		Actor: info.CreatedBy, SourcePlatform: info.SourcePlatform,
	}
	before := req
	if patch.Title != nil {
		req.Title = *patch.Title
	}
	if patch.ScheduleMode != nil {
		req.ScheduleMode = *patch.ScheduleMode
	}
	if patch.RunAt != nil {
		req.RunAt = *patch.RunAt
	}
	if patch.CronExpr != nil {
		req.CronExpr = *patch.CronExpr
	}
	if patch.TriggerMode != nil {
		req.TriggerMode = *patch.TriggerMode
	}
	if patch.Message != nil {
		req.Message = *patch.Message
	}
	if patch.ToolListNames != nil {
		req.ToolListNames = *patch.ToolListNames
	}
	if patch.ModelProvider != nil || patch.Model != nil {
		if patch.ModelProvider == nil || patch.Model == nil {
			return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: "model_provider and model must be updated together"}, nil
		}
		req.ModelProvider, req.Model = *patch.ModelProvider, *patch.Model
	}
	if patch.TargetID != nil {
		req.TargetID = *patch.TargetID
	}
	if patch.AllEnabledPlatforms != nil {
		req.AllEnabledPlatforms = *patch.AllEnabledPlatforms
	}
	if patch.Enabled != nil {
		req.Enabled = *patch.Enabled
	}
	prepared := s.prepare(req, false)
	if prepared.Status != operationresult.Success {
		return prepared, nil
	}
	after := prepared.Data.Definition
	prepared.Data.ResetDelivery = prepared.Data.ResetDelivery || after.Enabled && ((patch.Enabled != nil && *patch.Enabled) ||
		before.ScheduleMode != after.ScheduleMode || before.RunAt != after.RunAt || before.CronExpr != after.CronExpr ||
		before.TriggerMode != after.TriggerMode || before.Message != after.Message ||
		before.TargetID != after.TargetID || before.AllEnabledPlatforms != after.AllEnabledPlatforms ||
		before.ModelProvider != after.ModelProvider || before.Model != after.Model ||
		!slices.Equal(before.ToolListNames, after.ToolListNames))
	return prepared, nil
}

func (s *cronService) ListCrons(ctx context.Context, includeDisabled, includeCompleted bool) (operationresult.Result[[]CronInfo], error) {
	jobs, err := s.repo.List(ctx, includeDisabled)
	if err != nil || jobs.Status != operationresult.Success {
		return jobs, err
	}
	result := []CronInfo{}
	for _, job := range jobs.Data {
		if includeCompleted || !job.Completed {
			result = append(result, job)
		}
	}
	return operationresult.Result[[]CronInfo]{Status: operationresult.Success, Data: result}, nil
}

func (s *cronService) prepare(req CronCreateRequest, reset bool) operationresult.Result[CronSaveRequest] {
	req.Name, req.Title = strings.TrimSpace(req.Name), strings.TrimSpace(req.Title)
	req.Message, req.SourcePlatform = strings.TrimSpace(req.Message), strings.TrimSpace(req.SourcePlatform)
	req.RunAt, req.CronExpr = strings.TrimSpace(req.RunAt), strings.TrimSpace(req.CronExpr)
	req.ModelProvider, req.Model = strings.TrimSpace(req.ModelProvider), strings.TrimSpace(req.Model)
	req.TargetID = strings.TrimSpace(req.TargetID)
	req.ToolListNames = normalizeCronToolNames(req.ToolListNames)
	if req.Name == "" {
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: "cron name is required"}
	}
	if req.Title == "" {
		req.Title = req.Name
	}
	if req.Message == "" {
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: "message is required"}
	}
	switch req.TriggerMode {
	case crondef.TriggerDirect:
	case crondef.TriggerLLM:
		if _, err := elyph.ParseTask(req.Message, ""); err != nil {
			return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: err.Error()}
		}
	default:
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: fmt.Sprintf("unsupported trigger mode %q", req.TriggerMode)}
	}
	if req.SourcePlatform == "" {
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: "source platform is required"}
	}
	if req.TargetID != "" && req.AllEnabledPlatforms {
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: "target_id cannot be combined with all_enabled_platforms"}
	}
	if req.ModelProvider != "" || req.Model != "" {
		if req.ModelProvider == "" || req.Model == "" {
			return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: "model_provider and model must both be set or cleared"}
		}
		if result := s.validateModel(req.ModelProvider, req.Model); result.Status != operationresult.Success {
			return operationresult.Result[CronSaveRequest]{Status: result.Status, Message: result.Message}
		}
	}
	runAt := req.RunAt
	expr, next, err := cronSchedule(&req, s.now())
	if err != nil {
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: err.Error()}
	}
	reset = reset || req.RunAt != runAt
	return operationresult.Result[CronSaveRequest]{Status: operationresult.Success, Data: CronSaveRequest{Definition: req, Schedule: expr, NextRunAt: next, ResetDelivery: reset}}
}

func cronSchedule(req *CronCreateRequest, now time.Time) (string, *time.Time, error) {
	var next *time.Time
	expr := req.CronExpr
	switch req.ScheduleMode {
	case crondef.ScheduleOnce:
		runAt, err := time.ParseInLocation(time.DateTime, req.RunAt, time.Local)
		if err != nil {
			return "", nil, fmt.Errorf("run_at must use YYYY-MM-DD HH:MM:SS: %w", err)
		}
		if req.Enabled {
			nowMinute, runMinute := now.Truncate(time.Minute), runAt.Truncate(time.Minute)
			if runMinute.Before(nowMinute) {
				return "", nil, fmt.Errorf("run_at 时间不正确：一次性 cron 是分钟级调度，必须晚于当前这一分钟。当前时间：%s", now.Format(time.DateTime))
			}
			if runMinute.Equal(nowMinute) {
				runAt = nowMinute.Add(time.Minute)
				req.RunAt = runAt.Format(time.DateTime)
			}
			next = &runAt
		}
		expr = fmt.Sprintf("%d %d %d %d *", runAt.Minute(), runAt.Hour(), runAt.Day(), int(runAt.Month()))
	case crondef.ScheduleCron:
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
	if req.Enabled && req.ScheduleMode == crondef.ScheduleCron {
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

func (t CronTool) Definition() toolshare.ToolDefinition {
	builder := toolshare.NewBuilder("cron").Weavable().Description("定时任务入口；查询→cron_query，修改→cron_write；调用本工具仅返回指引和当前时间。").Risk(toolshare.RiskMedium).SuperadminOnly().DependsOn("cron_query", "cron_write")
	definition := builder.Build(t.PrepareCall)
	definition.DiscoveryContent = func(context.Context) (string, bool, error) {
		content, override := t.DiscoveryContent()
		return content, override, nil
	}
	return definition
}
func (t CronTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.TextConfirmationDetail(info.Description)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		now := t.now().Format(time.DateTime)
		result := &toolshare.Result{Content: strings.TrimSpace("cron 是定时任务管理入口。当前本地时间：" + now + "。请调用 cron_query 或 cron_write。cron_query 不传 name 时列出 cron，传 name 时查询单个 cron。")}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, "cron guide returned; current_time=" + now, nil
	})}, nil
}

// DiscoveryContent appends the current local time to the discover_tool text.
// The cron entry tool is the natural place to surface this because users often
// discover cron tools to set up time-based reminders.
func (t CronTool) DiscoveryContent() (string, bool) {
	return "当前本地时间：" + t.now().Format(time.DateTime), false
}

func (t CronQueryTool) Definition() toolshare.ToolDefinition {
	builder := cronBuilder("cron_query", "查定时任务；name空→全列表，否则→单个。").Hidden()

	return builder.
		String("name", "任务短名。").
		Boolean("include_disabled", "列表包含停用项，默认false。").
		Boolean("include_completed", "列表包含已完成项，默认false。").Build(t.PrepareCall)
}
func (t CronQueryTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args cronQueryArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "name", Label: "任务名", Value: args.Name},
		toolshare.ConfirmationField{Key: "include_disabled", Label: "包含停用", Value: args.IncludeDisabled},
		toolshare.ConfirmationField{Key: "include_completed", Label: "包含已完成", Value: args.IncludeCompleted},
	)}, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		if strings.TrimSpace(args.Name) != "" {
			queried, err := t.service.repo.GetByName(ctx, args.Name)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if queried.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: queried.Status, Message: queried.Message}, queried.Message, nil
			}
			view := queried.Data
			result := &toolshare.Result{Content: formatCronInfo(view), NeedsElyphRules: view.TriggerMode == crondef.TriggerLLM}
			logContent := fmt.Sprintf("name=%s enabled=%t completed=%t", view.Name, view.Enabled, view.Completed)
			return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, logContent, nil
		}
		listed, err := t.service.ListCrons(ctx, args.IncludeDisabled, args.IncludeCompleted)
		if err != nil {
			return operationresult.Result[*toolshare.Result]{}, err.Error(), err
		}
		if listed.Status != operationresult.Success {
			return operationresult.Result[*toolshare.Result]{Status: listed.Status, Message: listed.Message}, listed.Message, nil
		}
		views := listed.Data
		result := &toolshare.Result{Content: "没有符合条件的定时任务。"}
		logContent := fmt.Sprintf("returned=%d", len(views))
		var content []string
		for _, view := range views {
			content = append(content, formatCronInfo(view))
			if view.TriggerMode == crondef.TriggerLLM {
				result.NeedsElyphRules = true
			}
		}
		if len(content) > 0 {
			result.Content = strings.Join(content, "\n\n")
		}
		return operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: result}, logContent, nil
	})}, nil
}

func (t CronWriteTool) Definition() toolshare.ToolDefinition {
	builder := cronBuilder("cron_write", cronWriteDescription()).Risk(toolshare.RiskHigh).Hidden().NeedsElyphRules()

	return builder.
		String("operation", "disable=停用保留；delete=永久删除；暂停用disable。", toolshare.Required(), toolshare.Enum("create", "update", "disable", "delete")).
		String("name", "任务短名：字母/数字/下划线/点/横线。", toolshare.Required()).
		String("title", "展示标题；create默认name。").
		String("schedule_mode", "once⇒run_at或非零run_after_*；cron⇒cron_expr。", toolshare.Enum(crondef.ScheduleOnce, crondef.ScheduleCron)).
		String("run_at", "绝对时间YYYY-MM-DD HH:MM:SS。").
		String("cron_expr", "5字段：分 时 日 月 周。").
		Integer("run_after_minutes", "相对分钟数，按时长。").
		Integer("run_after_hours", "相对小时数，按时长。").
		Integer("run_after_days", "相对日历天数。").
		Integer("run_after_weeks", "相对周数，1周=7日历天。").
		Integer("run_after_months", "相对日历月数。").
		String("trigger_mode", "direct=直接通知；llm=后台LLM任务。", toolshare.Enum(crondef.TriggerDirect, crondef.TriggerLLM)).
		String("message", "direct→通知正文；llm→ELyph #task <name> - 描述 任务文本。").
		StringArray("tool_list_names", "llm允许的工具/Skill/标签名，无需@；仅所选能力+必要依赖，无discover_tool；update: []→清空。").
		String("model_provider", "与model同传；create省略→work模型；update两者同为\"\"→默认模型。").
		String("model", "模型名，与model_provider成对。").
		String("target_id", "来源平台的私聊接收人ID；create省略→默认接收人；update省略→保留，\"\"→恢复默认；不可与广播同时设置。").
		Boolean("all_enabled_platforms", "true→广播到所有已启用平台的超级管理员；create默认false。").
		Boolean("enabled", "create默认true；false→停用保留。").Build(t.PrepareCall)
}
func (t CronWriteTool) PrepareCall(ctx context.Context, req toolshare.CallRequest, info toolshare.Info) (operationresult.Result[*toolshare.PreparedCall], error) {
	var args cronWriteArgs
	if err := decodeArgs(req.Arguments, &args); err != nil {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	operation := strings.ToLower(strings.TrimSpace(args.Operation))
	if operation != "create" && operation != "update" && operation != "disable" && operation != "delete" {
		return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.InvalidArguments, Message: "operation must be create, update, disable, or delete"}, nil
	}
	args.Name = strings.TrimSpace(args.Name)
	details := []toolshare.ConfirmationDetailBlock{toolshare.FieldsConfirmationDetail(
		toolshare.ConfirmationField{Key: "operation", Label: "操作", Value: operation},
		toolshare.ConfirmationField{Key: "name", Label: "任务名", Value: args.Name},
	)}
	var save CronSaveRequest
	if operation == "create" || operation == "update" {
		var prepared operationresult.Result[CronSaveRequest]
		var err error
		if operation == "create" {
			prepared, err = t.prepareCreate(ctx, args)
		} else {
			prepared, err = t.prepareUpdate(ctx, args)
		}
		if err != nil {
			return operationresult.Result[*toolshare.PreparedCall]{}, err
		}
		if prepared.Status != operationresult.Success {
			return operationresult.Result[*toolshare.PreparedCall]{Status: prepared.Status, Message: prepared.Message}, nil
		}
		save = prepared.Data
		definition := save.Definition
		var nextRunAt any
		if save.NextRunAt != nil {
			nextRunAt = save.NextRunAt.Format(time.DateTime)
		}
		details = append(details, toolshare.FieldsConfirmationDetail(
			toolshare.ConfirmationField{Key: "title", Label: "标题", Value: definition.Title},
			toolshare.ConfirmationField{Key: "schedule_mode", Label: "调度模式", Value: definition.ScheduleMode},
			toolshare.ConfirmationField{Key: "run_at", Label: "绝对执行时间", Value: definition.RunAt},
			toolshare.ConfirmationField{Key: "cron_expr", Label: "Cron 表达式", Value: save.Schedule},
			toolshare.ConfirmationField{Key: "next_run_at", Label: "下次执行时间", Value: nextRunAt},
			toolshare.ConfirmationField{Key: "trigger_mode", Label: "触发模式", Value: definition.TriggerMode},
			toolshare.ConfirmationField{Key: "message", Label: "任务正文", Value: definition.Message},
			toolshare.ConfirmationField{Key: "tool_list_names", Label: "允许工具", Value: definition.ToolListNames},
			toolshare.ConfirmationField{Key: "model_provider", Label: "模型提供方", Value: definition.ModelProvider},
			toolshare.ConfirmationField{Key: "model", Label: "模型", Value: definition.Model},
			toolshare.ConfirmationField{Key: "target_id", Label: "接收人", Value: definition.TargetID},
			toolshare.ConfirmationField{Key: "all_enabled_platforms", Label: "广播", Value: definition.AllEnabledPlatforms},
			toolshare.ConfirmationField{Key: "enabled", Label: "启用", Value: definition.Enabled},
		))
	}
	return operationresult.Result[*toolshare.PreparedCall]{Status: operationresult.Success, Data: toolshare.NewPreparedCall(req, toolshare.RiskAssessment{Level: info.Risk}, details, func(ctx context.Context) (operationresult.Result[*toolshare.Result], string, error) {
		var result operationresult.Result[*toolshare.Result]
		var callErr error
		switch operation {
		case "create", "update":
			job, err := t.service.repo.Upsert(ctx, save)
			callErr = err
			result = operationresult.Result[*toolshare.Result]{Status: job.Status, Message: job.Message}
			if err == nil && job.Status == operationresult.Success {
				action := "已创建"
				if operation == "update" {
					action = "已更新"
				}
				result.Data = &toolshare.Result{Content: action + "定时任务：" + job.Data.Name + "\n" + formatCronInfo(job.Data)}
			}
		case "disable":
			disabled, err := t.service.repo.DisableByName(ctx, args.Name)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if disabled.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: disabled.Status, Message: disabled.Message}, disabled.Message, nil
			}
			result = operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: fmt.Sprintf("已停用定时任务：%s", args.Name)}}
		case "delete":
			deleted, err := t.service.repo.DeleteByName(ctx, args.Name)
			if err != nil {
				return operationresult.Result[*toolshare.Result]{}, err.Error(), err
			}
			if deleted.Status != operationresult.Success {
				return operationresult.Result[*toolshare.Result]{Status: deleted.Status, Message: deleted.Message}, deleted.Message, nil
			}
			result = operationresult.Result[*toolshare.Result]{Status: operationresult.Success, Data: &toolshare.Result{Content: fmt.Sprintf("已删除定时任务：%s", args.Name)}}
		}
		if callErr != nil {
			return operationresult.Result[*toolshare.Result]{}, fmt.Sprintf("operation=%s name=%s status=failed", operation, args.Name), callErr
		}
		if result.Status != operationresult.Success {
			return result, result.Message, nil
		}
		return result, fmt.Sprintf("operation=%s name=%s status=ok", operation, args.Name), nil
	})}, nil
}

func (t CronWriteTool) prepareCreate(ctx context.Context, args cronWriteArgs) (operationresult.Result[CronSaveRequest], error) {
	if (args.ModelProvider == nil) != (args.Model == nil) {
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: "model_provider and model must be provided together"}, nil
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
	allEnabledPlatforms := args.AllEnabledPlatforms != nil && *args.AllEnabledPlatforms
	targetID := ""
	if args.TargetID != nil {
		targetID = *args.TargetID
	}
	runAt, err := resolveRunAtAt(args.RunAt, relativeOffset{Minutes: args.RunAfterMinutes, Hours: args.RunAfterHours, Days: args.RunAfterDays, Weeks: args.RunAfterWeeks, Months: args.RunAfterMonths}, t.now())
	if err != nil {
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	return t.service.prepare(CronCreateRequest{Name: args.Name, Title: args.Title, ScheduleMode: args.ScheduleMode, RunAt: runAt, CronExpr: args.CronExpr, TriggerMode: args.TriggerMode, Message: args.Message, ToolListNames: args.ToolListNames, ModelProvider: provider, Model: model, TargetID: targetID, AllEnabledPlatforms: allEnabledPlatforms, Enabled: enabled, Actor: actor, SourcePlatform: actor.Platform}, true), nil
}

func (t CronWriteTool) prepareUpdate(ctx context.Context, args cronWriteArgs) (operationresult.Result[CronSaveRequest], error) {
	runAt, err := resolveUpdateRunAtAt(stringPtrIfNotEmpty(args.RunAt), relativeOffset{Minutes: args.RunAfterMinutes, Hours: args.RunAfterHours, Days: args.RunAfterDays, Weeks: args.RunAfterWeeks, Months: args.RunAfterMonths}, t.now())
	if err != nil {
		return operationresult.Result[CronSaveRequest]{Status: operationresult.InvalidArguments, Message: err.Error()}, nil
	}
	patch := CronUpdateRequest{Name: args.Name, Title: stringPtrIfNotEmpty(args.Title), RunAt: runAt, CronExpr: stringPtrIfNotEmpty(args.CronExpr), Message: stringPtrIfNotEmpty(args.Message), ModelProvider: args.ModelProvider, Model: args.Model, TargetID: args.TargetID, AllEnabledPlatforms: args.AllEnabledPlatforms, Enabled: args.Enabled}
	if args.ScheduleMode != "" {
		patch.ScheduleMode = &args.ScheduleMode
	}
	if args.TriggerMode != "" {
		patch.TriggerMode = &args.TriggerMode
	}
	if args.ToolListNames != nil {
		patch.ToolListNames = &args.ToolListNames
	}
	return t.service.prepareUpdate(ctx, patch)
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

func resolveRunAtAt(runAt string, offset relativeOffset, now time.Time) (string, error) {
	runAt = strings.TrimSpace(runAt)
	if !offset.hasAny() {
		return runAt, nil
	}
	if runAt != "" {
		return "", fmt.Errorf("run_at 不能和 run_after_* 同时传；请只使用绝对时间或相对偏移之一")
	}
	if offset.Minutes < 0 || offset.Hours < 0 || offset.Days < 0 || offset.Weeks < 0 || offset.Months < 0 {
		return "", fmt.Errorf("run_after_* 必须是正数；当前本地时间：%s", now.Format(time.DateTime))
	}
	run := now.AddDate(0, offset.Months, offset.Weeks*7+offset.Days).Add(time.Duration(offset.Hours)*time.Hour + time.Duration(offset.Minutes)*time.Minute)
	return run.Format(time.DateTime), nil
}

func cronWriteDescription() string {
	return `写定时任务。
create⇒schedule_mode,trigger_mode,message必填。
update：省略→保持原值；普通字符串""→保持原值(model_provider/model/target_id除外)；false有效。
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

func cronBuilder(name, description string) *toolshare.Builder {
	return toolshare.NewBuilder(name).Weavable().Description(description).Risk(toolshare.RiskMedium).SuperadminOnly()
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

func formatCronInfo(job CronInfo) string {
	content := fmt.Sprintf(
		"name:%s\ntitle:%s\nschedule_mode:%s\nrun_at:%s\ncron_expr:%s\ntrigger_mode:%s\ntool_list_names:%s\nmodel_provider:%s\nmodel:%s\ntarget_id:%s\nall_enabled_platforms:%t\nsource_platform:%s\ncreated_by:%+v\nenabled:%t\ncompleted:%t\nrun_count:%d\ncreated_at:%s\nupdated_at:%s",
		job.Name, job.Title, job.ScheduleMode, job.RunAt, job.CronExpr, job.TriggerMode,
		strings.Join(job.ToolListNames, ", "), job.ModelProvider, job.Model,
		job.TargetID, job.AllEnabledPlatforms, job.SourcePlatform, job.CreatedBy,
		job.Enabled, job.Completed, job.RunCount,
		job.CreatedAt.Format(time.RFC3339Nano), job.UpdatedAt.Format(time.RFC3339Nano),
	)
	if job.LastRunAt != nil {
		content += "\nlast_run_at:" + job.LastRunAt.Format(time.RFC3339Nano)
	}
	if job.NextRunAt != nil {
		content += "\nnext_run_at:" + job.NextRunAt.Format(time.RFC3339Nano)
	}
	return content + "\nmessage:\n" + job.Message
}
