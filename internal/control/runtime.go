package control

import (
	"context"
	"sync"

	"elbot/internal/config"
)

type DoctorIssue struct {
	Path    string       `json:"path"`
	Level   config.Level `json:"level"`
	Topic   string       `json:"topic"`
	Message string       `json:"message"`
	Field   []string     `json:"field"`
	Missing bool         `json:"missing"`
}
type CheckConfigurationResult struct {
	ConfigPath string        `json:"config_path"`
	Items      []DoctorIssue `json:"items"`
}

func (m *Supervisor) CheckConfiguration(ctx context.Context) (CheckConfigurationResult, error) {
	if err := authorize(ctx, true, m.deps.Doctor != nil); err != nil {
		return CheckConfigurationResult{}, err
	}
	report, err := m.deps.Doctor.Check(ctx)
	if err != nil {
		return CheckConfigurationResult{}, ClassifyError(err)
	}
	result := CheckConfigurationResult{ConfigPath: report.ConfigPath, Items: []DoctorIssue{}}
	for _, file := range report.Files {
		for _, issue := range file.Issues {
			result.Items = append(result.Items, DoctorIssue{Path: file.Path, Level: issue.Level, Topic: issue.Topic, Message: issue.Message, Field: append([]string(nil), issue.Field...), Missing: issue.Missing})
		}
	}
	return result, nil
}

func (m *Supervisor) RequestRestart(ctx context.Context) (RequestRestartResult, error) {
	if err := authorize(ctx, true, m.deps.Runtime != nil); err != nil {
		return RequestRestartResult{}, err
	}
	prepared, err := m.deps.Runtime.PrepareRestart(ctx)
	if err != nil {
		return RequestRestartResult{}, ClassifyError(err)
	}
	// Commit / Cancel 互斥且只消费一次；不在请求处理期间停止自己的 HTTP 或消息入口。
	var once sync.Once
	var commitErr error
	result := RequestRestartResult{Accepted: true}
	result.Commit = func(ctx context.Context) error {
		once.Do(func() {
			if err := ctx.Err(); err != nil {
				prepared.Cancel()
				commitErr = ClassifyError(err)
				return
			}
			commitErr = ClassifyError(prepared.Commit(ctx))
		})
		return commitErr
	}
	result.Cancel = func() {
		once.Do(func() {
			prepared.Cancel()
			commitErr = &Error{Code: "canceled", Message: "重启结果未交付，预留已取消"}
		})
	}
	return result, nil
}
