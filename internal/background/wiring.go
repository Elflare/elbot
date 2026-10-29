// 拆分完整配置，构造内部组件并连接外部公开能力。
package background

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	elcron "elbot/internal/background/cron"
	"elbot/internal/background/elnis"
	"elbot/internal/background/execution"
	"elbot/internal/background/maintenance"
	"elbot/internal/background/share"
	"elbot/internal/config"
	globalevents "elbot/internal/events"
	"elbot/internal/media"
	"elbot/internal/signal"
	"elbot/internal/storage"
	"elbot/internal/tool"
)

// 根部只为存储编码和业务数据转换保留适配；公开数据类型按需复用，能力按消费方职责裁剪。
// Cron 与 Elnis 使用自有记录，根部将存储及媒体能力转换为内部契约。
type cronBridge struct {
	repo storage.CronJobRepository // 请求 Storage 读写任务定义和执行状态，转换为内部记录。
}

type elnisBridge struct {
	repo  storage.ElnisEventRepository // 请求 Storage 持久化事件及报告，转换为内部记录。
	media *media.MediaCenter           // 转换事件媒体处理的请求与结果。
}

var _ elnis.EventRepository = elnisBridge{}
var _ elnis.Media = elnisBridge{}

func elnisError(err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return elnis.ErrEventNotFound
	}
	return err
}

func elnisEvent(row *storage.ElnisEvent) *elnis.EventRecord {
	return &elnis.EventRecord{ID: row.ID, EventKey: row.EventKey, ContentHash: row.ContentHash,
		ResolvedTargets: row.ResolvedTargets, SessionID: row.SessionID, Result: row.Result}
}

func (b elnisBridge) Create(ctx context.Context, req elnis.CreateEventRequest) (*elnis.EventRecord, error) {
	row, err := b.repo.Create(ctx, storage.CreateElnisEventRequest{
		MediaIDs: req.MediaIDs, EventKey: req.EventKey, TokenName: req.TokenName, ElwispName: req.ElwispName,
		Source: req.Source, SourceID: req.SourceID, Tags: req.Tags, Mode: req.Mode, ModelSlot: req.ModelSlot,
		ContentHash: req.ContentHash, ToolDeclarations: req.ToolDeclarations, ToolHash: req.ToolHash,
		RequestedTargets: req.RequestedTargets, ResolvedTargets: req.ResolvedTargets, Status: req.Status,
		ReceivedAt: req.ReceivedAt, CreatedAt: req.CreatedAt,
	})
	if err != nil {
		return nil, elnisError(err)
	}
	return elnisEvent(row), nil
}

func (b elnisBridge) Get(ctx context.Context, id string) (*elnis.EventRecord, error) {
	row, err := b.repo.Get(ctx, id)
	if err != nil {
		return nil, elnisError(err)
	}
	return elnisEvent(row), nil
}

func (b elnisBridge) GetByKey(ctx context.Context, name, source, sourceID string) (*elnis.EventRecord, error) {
	row, err := b.repo.GetByKey(ctx, name, source, sourceID)
	if err != nil {
		return nil, elnisError(err)
	}
	return elnisEvent(row), nil
}

func (b elnisBridge) Update(ctx context.Context, req elnis.UpdateEventRequest) error {
	return elnisError(b.repo.Update(ctx, storage.UpdateElnisEventRequest{ID: req.ID, ResolvedTargets: req.ResolvedTargets,
		Status: req.Status, SessionID: req.SessionID, Result: req.Result, Error: req.Error}))
}

func (b elnisBridge) FailInterrupted(ctx context.Context, statuses []string, failed, reason string) error {
	return elnisError(b.repo.FailInterrupted(ctx, statuses, failed, reason))
}

func (b elnisBridge) PrepareReport(ctx context.Context, req elnis.PrepareReportRequest) error {
	items := make([]storage.CreateElnisReportDeliveryRequest, 0, len(req.Deliveries))
	for _, item := range req.Deliveries {
		items = append(items, storage.CreateElnisReportDeliveryRequest{Target: item.Target, Output: item.Output, MessageID: item.MessageID})
	}
	return elnisError(b.repo.PrepareReport(ctx, storage.PrepareElnisReportRequest{EventID: req.EventID,
		ResolvedTargets: req.ResolvedTargets, SessionID: req.SessionID, Result: req.Result,
		Deliveries: items, ResultReadyStatus: req.ResultReadyStatus}))
}

func (b elnisBridge) ResetDeliveringReports(ctx context.Context, delivering, ready string) error {
	return elnisError(b.repo.ResetDeliveringReports(ctx, delivering, ready))
}

func (b elnisBridge) ListResultReadyReportIDs(ctx context.Context, ready string) ([]string, error) {
	ids, err := b.repo.ListResultReadyReportIDs(ctx, ready)
	return ids, elnisError(err)
}

func (b elnisBridge) ClaimReport(ctx context.Context, id, ready, delivering string) (bool, error) {
	claimed, err := b.repo.ClaimReport(ctx, id, ready, delivering)
	return claimed, elnisError(err)
}

func (b elnisBridge) ReleaseReport(ctx context.Context, id, delivering, ready, reason string) error {
	return elnisError(b.repo.ReleaseReport(ctx, id, delivering, ready, reason))
}

func (b elnisBridge) ListReportDeliveries(ctx context.Context, id string) ([]elnis.ReportDelivery, error) {
	rows, err := b.repo.ListReportDeliveries(ctx, id)
	if err != nil {
		return nil, elnisError(err)
	}
	items := make([]elnis.ReportDelivery, 0, len(rows))
	for _, row := range rows {
		items = append(items, elnis.ReportDelivery{ID: row.ID, Target: row.Target, Output: row.Output, MessageID: row.MessageID, Status: row.Status})
	}
	return items, nil
}

func (b elnisBridge) StartReportDelivery(ctx context.Context, id string) error {
	return elnisError(b.repo.StartReportDelivery(ctx, id))
}

func (b elnisBridge) MarkReportDeliveryFailed(ctx context.Context, eventID, deliveryID, ready, reason, receipt string, mappings []share.ReportMessageMap) error {
	return elnisError(b.repo.MarkReportDeliveryFailed(ctx, eventID, deliveryID, ready, reason, receipt, reportMessageMaps(mappings)))
}

func (b elnisBridge) MarkReportDeliveriesDelivered(ctx context.Context, ids []string, receipt string, mappings []share.ReportMessageMap) error {
	return elnisError(b.repo.MarkReportDeliveriesDelivered(ctx, ids, receipt, reportMessageMaps(mappings)))
}

func (b elnisBridge) CompleteReport(ctx context.Context, id, delivering, completed string) error {
	return elnisError(b.repo.CompleteReport(ctx, id, delivering, completed))
}

func reportMessageMaps(mappings []share.ReportMessageMap) []storage.PlatformMessageMap {
	rows := make([]storage.PlatformMessageMap, 0, len(mappings))
	for _, mapping := range mappings {
		rows = append(rows, storage.PlatformMessageMap{Platform: mapping.Platform, PlatformScopeID: mapping.PlatformScopeID,
			PlatformMessageID: mapping.PlatformMessageID, SessionID: mapping.SessionID, MessageID: mapping.MessageID})
	}
	return rows
}

func (b elnisBridge) Info(ctx context.Context, id string) (media.MediaInfo, error) {
	return b.media.Info(ctx, id)
}

func (b elnisBridge) ImportURL(ctx context.Context, url string, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	return b.media.ImportURL(ctx, url, input, limits)
}

func (b elnisBridge) ImportReader(ctx context.Context, reader io.Reader, size int64, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	return b.media.ImportReader(ctx, reader, size, input, limits)
}

func (b elnisBridge) ImportBytes(ctx context.Context, data []byte, input media.Input, limits media.ImportLimits) (media.MediaInfo, error) {
	return b.media.ImportBytes(ctx, data, input, limits)
}

func (b elnisBridge) AddEventReference(ctx context.Context, mediaID, eventID, purpose string) error {
	return b.media.AddReference(ctx, media.Reference{MediaID: mediaID, OwnerType: "elnis_event", OwnerID: eventID, Purpose: purpose})
}

// NewSupervisor 只构造与接线；失败仍返回已取得的总管，由 App 统一清理。
func NewSupervisor(ctx context.Context, cfg *config.Config, deps SupervisorDeps) (*Supervisor, error) {
	ctx, cancel := context.WithCancel(ctx)
	m := &Supervisor{ctx: ctx, cancel: cancel, cfg: cfg, failed: make(chan struct{}), done: make(chan struct{})}
	if err := ctx.Err(); err != nil {
		return m, err
	}

	// Execution 只声明 Agent.RunBackground；Runner 准备任务目录和后台来源。
	// 会话与工具准备由 Agent 完成，报告解析和重试由 Cron、Elnis 负责。
	m.runner = execution.NewBackgroundRunner(execution.BackgroundRunnerDeps{
		Execution: deps.Agent, SandboxRoot: cfg.Sandbox.Root,
	})
	cron := cronBridge{repo: deps.Store.CronJobs()}
	// Models 只暴露 ResolveMode 和 ValidateSelection，发送与复制使用独立函数。
	m.cronManager = elcron.NewManager(cron)
	m.cron = elcron.NewService(elcron.Options{
		Manager: m.cronManager, Store: cron, Runner: m.runner,
		SendTarget: deps.Platform.SendTo, Models: deps.Models, Media: deps.Media, CopyBackground: deps.Agent.CopyBackground, SessionTakenOver: deps.Agent.SessionTakenOver,
		EnabledPlatforms: enabledCronPlatforms(cfg), SandboxRoot: cfg.Sandbox.Root,
	})
	if err := m.cronManager.RegisterHandler(elcron.UserHandlerName, m.cron.Handler); err != nil {
		return m, fmt.Errorf("register cron handler: %w", err)
	}

	// 维护组件只接收日志最小接口、两项清理函数和媒体 Cleanup 接口。
	maint := maintenance.NewServiceWithConfig(deps.Logs, deps.ChatHistory.DeleteBefore, cfg, deps.Agent.CleanupSessions)
	maint.Media = deps.Media
	if err := maint.RegisterCronHandlers(m.cronManager.RegisterMaintenanceHandler); err != nil {
		return m, fmt.Errorf("register maintenance handlers: %w", err)
	}

	var tokens map[string]string
	if cfg.Elnis.Enabled {
		var err error
		tokens, err = resolveElnisTokens(cfg)
		if err != nil {
			return m, err
		}
	}
	var platforms []string
	for _, target := range enabledCronPlatforms(cfg) {
		platforms = append(platforms, target.Name)
	}
	elnisDeps := elnisBridge{repo: deps.Store.ElnisEvents(), media: deps.Media}
	// ToolPreloader 只声明 BackgroundSelections，使用 Tool 根包类型。
	// 平台能力分别注入发送与按名称调用 API 的函数，模型解析只返回所需选择数据。
	service, err := elnis.NewService(elnis.Options{
		Media: elnisDeps, Config: cfg.Elnis, SandboxRoot: cfg.Sandbox.Root, Tokens: tokens,
		Store: elnisDeps, EnabledPlatforms: platforms, CallPlatformAPI: deps.Platform.CallPlatformAPI,
		Send: deps.Platform.SendTo, Runner: m.runner, ToolPreloader: deps.Tools, SessionTakenOver: deps.Agent.SessionTakenOver,
		ResolveModel: func(slot string) config.ModelSelection {
			return deps.Models.ResolveMode(slot).ModelSelection
		},
	})
	if err != nil {
		return m, fmt.Errorf("build elnis: %w", err)
	}
	// 禁用 Elnis 时也保留恢复入口；Start 只在启用时启动监听和 workers。
	m.elnis = elnis.NewRuntime(cfg.Elnis.HTTP, service)

	// 变更回调只登记名称，由 Manager 串行读库刷新。
	connection, err := deps.Tools.ObserveCronChanged(func(event tool.CronChangedEvent) error {
		return m.cronManager.RefreshJob(event.Name)
	}, signal.ConnectOptions{})
	if err != nil {
		return m, fmt.Errorf("observe cron changes: %w", err)
	}
	m.connections = append(m.connections, connection)

	// App 在全部构造完成后启动 Platform；此时已订阅，避免漏掉启动时的连接通知。
	// Service.Start 前按平台缓存通知，根部完成前置启动后才放行消费。
	connection, err = globalevents.PlatformConnected.Observe(func(ctx context.Context, event globalevents.PlatformConnectedEvent) error {
		return m.cron.EnqueuePlatformConnected(ctx, event.Platform)
	}, signal.ConnectOptions{})
	if err != nil {
		return m, fmt.Errorf("observe platform connections: %w", err)
	}
	m.connections = append(m.connections, connection)
	return m, nil
}

func enabledCronPlatforms(cfg *config.Config) []elcron.PlatformTarget {
	out := []elcron.PlatformTarget{}
	for name, raw := range cfg.Platform {
		if !platformConfigEnabled(raw) {
			continue
		}
		out = append(out, elcron.PlatformTarget{Name: name, SuperadminIDs: cfg.Security.Superadmins[name]})
	}
	return out
}

func platformConfigEnabled(raw map[string]any) bool {
	value, ok := raw["enabled"]
	if !ok {
		return false
	}
	enabled, ok := value.(bool)
	return ok && enabled
}

func resolveElnisTokens(cfg *config.Config) (map[string]string, error) {
	out := map[string]string{}
	configDir := filepath.Dir(cfg.ConfigPath)
	for name, tokenCfg := range cfg.Elnis.Tokens {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		for _, envName := range tokenCfg.TokenEnv {
			envName = strings.TrimSpace(envName)
			if envName == "" {
				continue
			}
			value, ok, err := config.ConfigEnv(envName, configDir)
			if err != nil {
				return nil, fmt.Errorf("resolve elnis token %q from %s: %w", name, envName, err)
			}
			if ok && strings.TrimSpace(value) != "" {
				out[name] = strings.TrimSpace(value)
				break
			}
		}
	}
	return out, nil
}

func cronJob(row *storage.CronJob) *elcron.Job {
	return &elcron.Job{ID: row.ID, Name: row.Name, Handler: row.Handler, Schedule: row.Schedule, Enabled: row.Enabled,
		Metadata: row.Metadata, DeliveryState: row.DeliveryState, DeliveryToken: row.DeliveryToken, NextRunAt: row.NextRunAt}
}

func cronError(err error) error {
	if errors.Is(err, storage.ErrNotFound) {
		return elcron.ErrJobNotFound
	}
	return err
}

func (b cronBridge) Upsert(ctx context.Context, req elcron.UpsertJobRequest) (*elcron.Job, error) {
	row, err := b.repo.Upsert(ctx, storage.UpsertCronJobRequest{Name: req.Name, Handler: req.Handler, Schedule: req.Schedule, Enabled: req.Enabled, NextRunAt: req.NextRunAt})
	if err != nil {
		return nil, cronError(err)
	}
	return cronJob(row), nil
}
func (b cronBridge) GetByName(ctx context.Context, name string) (*elcron.Job, error) {
	row, err := b.repo.GetByName(ctx, name)
	if err != nil {
		return nil, cronError(err)
	}
	return cronJob(row), nil
}
func (b cronBridge) ListEnabled(ctx context.Context) ([]elcron.Job, error) {
	rows, err := b.repo.ListEnabled(ctx)
	if err != nil {
		return nil, cronError(err)
	}
	jobs := make([]elcron.Job, 0, len(rows))
	for i := range rows {
		jobs = append(jobs, *cronJob(&rows[i]))
	}
	return jobs, nil
}
func (b cronBridge) UpdateNextRunAt(ctx context.Context, id string, next *time.Time, updated time.Time) error {
	return cronError(b.repo.UpdateNextRunAt(ctx, id, next, updated))
}
func (b cronBridge) UpdateRunState(ctx context.Context, id string, state elcron.JobRunState) error {
	return cronError(b.repo.UpdateRunState(ctx, id, storage.CronJobRunState{LastRunAt: state.LastRunAt, UpdatedAt: state.UpdatedAt}))
}
func (b cronBridge) CompareAndSwapDelivery(ctx context.Context, id, expected, next, state string, mappings []share.ReportMessageMap) (bool, error) {
	swapped, err := b.repo.CompareAndSwapDelivery(ctx, id, expected, next, state, reportMessageMaps(mappings))
	return swapped, cronError(err)
}
func (b cronBridge) DisableByName(ctx context.Context, name string) error {
	return cronError(b.repo.DisableByName(ctx, name))
}
func (b cronBridge) DisableByNameIfDeliveryToken(ctx context.Context, name, token string) (bool, error) {
	disabled, err := b.repo.DisableByNameIfDeliveryToken(ctx, name, token)
	return disabled, cronError(err)
}
