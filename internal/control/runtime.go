package control

import (
	"context"
	"elbot/internal/control/share"
	"elbot/internal/operationresult"
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

func (m *Supervisor) CheckConfiguration(ctx context.Context) (operationresult.Result[CheckConfigurationResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[CheckConfigurationResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[CheckConfigurationResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	report, err := m.deps.Doctor.Check(ctx)
	if err != nil {
		return operationresult.Result[CheckConfigurationResult]{}, err
	}
	result := CheckConfigurationResult{ConfigPath: report.ConfigPath, Items: []DoctorIssue{}}
	for _, file := range report.Files {
		for _, issue := range file.Issues {
			result.Items = append(result.Items, DoctorIssue{Path: file.Path, Level: issue.Level, Topic: issue.Topic, Message: issue.Message, Field: append([]string(nil), issue.Field...), Missing: issue.Missing})
		}
	}
	return operationresult.Result[CheckConfigurationResult]{Status: operationresult.Success, Data: result}, nil
}

func (m *Supervisor) RequestRestart(ctx context.Context) (operationresult.Result[RequestRestartResult], error) {
	authorized, err := share.Authorize(ctx, true)
	if err != nil {
		return operationresult.Result[RequestRestartResult]{}, err
	}
	if authorized.Status != operationresult.Success {
		return operationresult.Result[RequestRestartResult]{Status: authorized.Status, Message: authorized.Message}, nil
	}
	prepared, err := m.deps.Runtime.PrepareRestart(ctx)
	if err != nil {
		return operationresult.Result[RequestRestartResult]{}, err
	}
	if prepared.Status != operationresult.Success {
		return operationresult.Result[RequestRestartResult]{Status: prepared.Status, Message: prepared.Message}, nil
	}
	preparation := prepared.Data
	var once sync.Once
	var commitErr error
	result := RequestRestartResult{Accepted: true}
	result.Commit = func(ctx context.Context) error {
		once.Do(func() {
			if err := ctx.Err(); err != nil {
				preparation.Cancel()
				commitErr = err
				return
			}
			commitErr = preparation.Commit(ctx)
		})
		return commitErr
	}
	result.Cancel = func() {
		once.Do(preparation.Cancel)
	}
	return operationresult.Result[RequestRestartResult]{Status: operationresult.Success, Data: result}, nil
}
