package cron

import (
	"context"
	background "elbot/internal/background/share"
	"errors"
	"time"
)

var ErrJobNotFound = errors.New("cron job not found")

// Job 是调度与执行需要的任务快照；JSON 由 Cron 按任务解释。
type Job struct {
	ID            string     // 持久化任务身份，同名重建后身份不同。
	Name          string     // 调度和变更通知使用的任务名称。
	Handler       string     // 注册处理器名称。
	Schedule      string     // 调度表达式。
	Enabled       bool       // 当前任务是否启用。
	Metadata      string     // 任务定义的 JSON。
	DeliveryState string     // 报告与逐项投递进度的 JSON。
	DeliveryToken string     // 投递条件提交令牌。
	NextRunAt     *time.Time // 已保存的下次运行时间。
}

// JobRunState 只允许记录本次运行事实；累计次数由存储原子递增。
type JobRunState struct {
	LastRunAt time.Time // 本次执行开始时间。
	UpdatedAt time.Time // 运行事实记录时间。
}

// UpsertJobRequest 只用于维护任务配置，不修改用户任务的元数据和投递进度。
type UpsertJobRequest struct {
	Name      string     // 维护任务名称。
	Handler   string     // 维护处理器名称。
	Schedule  string     // 调度表达式。
	Enabled   bool       // 配置的启用状态。
	NextRunAt *time.Time // Manager 计算的初次运行时间。
}

// JobRepository 是 Cron 独用的持久化能力，由 Background 根部转换实现。
type JobRepository interface {
	Upsert(context.Context, UpsertJobRequest) (*Job, error)
	GetByName(context.Context, string) (*Job, error)
	ListEnabled(context.Context) ([]Job, error)
	UpdateNextRunAt(context.Context, string, *time.Time, time.Time) error
	UpdateRunState(context.Context, string, JobRunState) error
	CompareAndSwapDelivery(context.Context, string, string, string, string, []background.ReportMessageMap) (bool, error)
	DisableByName(context.Context, string) error
	DisableByNameIfDeliveryToken(context.Context, string, string) (bool, error)
}
