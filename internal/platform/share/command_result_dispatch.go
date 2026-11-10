package share

import (
	command "elbot/internal/command"
	"elbot/internal/control"
	controlformat "elbot/internal/control/format"
	"encoding/json"
	"fmt"
	"strings"
)

func FormatCommandResult(req command.Request, result *command.Result) string {
	if result == nil {
		return ""
	}
	fields := strings.Fields(req.Args)
	switch result.Kind {
	case command.ResultKindText:
		if req.Name == "detail" {
			return formatCommandData(result.Data, func(d control.CurrentConfirmationResult) string {
				parts := make([]string, 0, len(d.Detail))
				for _, block := range d.Detail {
					if block.Kind == "arguments" {
						if data, err := json.MarshalIndent(block.Content, "", "  "); err == nil {
							parts = append(parts, string(data))
						}
					} else if text, ok := block.Content.(string); ok {
						parts = append(parts, text)
					}
				}
				return fmt.Sprintf("%s [%s]\n%s", d.ToolName, d.Risk, strings.Join(parts, "\n"))
			})
		}
		if req.Name == "confirm" || req.Name == "confirmtool" || req.Name == "confirmall" || req.Name == "reject" {
			return formatCommandData(result.Data, func(d control.RespondConfirmationResult) string {
				if !d.Accepted {
					return ""
				}
				if req.Name == "reject" {
					return "已拒绝"
				}
				return "已确认"
			})
		}
		return formatCommandData(result.Data, func(d command.TextData) string { return d.Content })
	case command.ResultKindSession, command.ResultKindCompact:
		return formatCommandData(result.Data, func(d control.SessionChangeResult) string {
			if result.Kind == command.ResultKindCompact {
				return "compacted → " + d.SessionID
			}
			if result.Confirmation != nil {
				if req.Name == "clean" {
					return "永久删除过期且未归档、未置顶的会话？"
				}
				return fmt.Sprintf("%s? %s · %s", req.Name, d.Session.Title, d.SessionID)
			}
			return controlformat.SessionChange(req.Name, d, controlformat.MessagesOptions{PreviewLimit: 80})
		})
	case command.ResultKindSessions:
		return formatCommandData(result.Data, func(d control.ListSessionsResult) string {
			opts := controlformat.SessionsOptions{MessageLimit: 4, PreviewLimit: 24}
			pageCommand := func(page int) string {
				args := fmt.Sprint(page)
				if req.Name == "resume" {
					args = "--page " + args
				} else if d.Query != "" {
					args += " -k " + d.Query
				}
				return commandUsage(req, req.Name, args)
			}
			if d.Page.Page > 1 {
				opts.PrevCommand = pageCommand(d.Page.Page - 1)
			}
			if d.HasNext {
				opts.NextCommand = pageCommand(d.Page.Page + 1)
			}
			return controlformat.Sessions(d, opts)
		})
	case command.ResultKindMessages:
		return formatCommandData(result.Data, func(d control.SessionMessagesResult) string {
			return controlformat.Messages(d, controlformat.MessagesOptions{PreviewLimit: 40})
		})
	case command.ResultKindStatus:
		return formatCommandData(result.Data, controlformat.Status)
	case command.ResultKindModel:
		return formatCommandData(result.Data, controlformat.Model)
	case command.ResultKindModels:
		return formatCommandData(result.Data, controlformat.Models)
	case command.ResultKindRequests:
		return formatCommandData(result.Data, func(d control.ListRequestsResult) string {
			return controlformat.Requests(d, controlformat.RequestsOptions{ShowSessionTitle: true})
		})
	case command.ResultKindStop:
		return formatCommandData(result.Data, controlformat.Stop)
	case command.ResultKindTools:
		if len(fields) > 0 {
			switch fields[0] {
			case "reload":
				return "skills reloaded"
			case "remove", "uninstall":
				if result.Confirmation != nil {
					return fmt.Sprintf("删除 %q 及目录？", fields[1])
				}
				return "removed: " + fields[1]
			}
		}
		return formatCommandData(result.Data, controlformat.Tools)
	case command.ResultKindRollback:
		return formatCommandData(result.Data, controlformat.Rollback)
	case command.ResultKindRollbacks:
		return formatCommandData(result.Data, controlformat.Rollbacks)
	case command.ResultKindHelp:
		return formatCommandData(result.Data, func(d control.ListCommandsResult) string { return controlformat.Help(req.Prefix, d) })
	case command.ResultKindHooks:
		if len(fields) > 0 {
			switch fields[0] {
			case "reload":
				return formatCommandData(result.Data, controlformat.HookReload)
			case "start", "stop", "restart":
				return fmt.Sprintf("hook %s: %s · OK", fields[0], fields[1])
			default:
				return formatCommandData(result.Data, controlformat.HookDetail)
			}
		}
		return formatCommandData(result.Data, controlformat.Hooks)
	case command.ResultKindLogs:
		return formatCommandData(result.Data, func(d control.QueryLogsResult) string {
			return controlformat.Logs(map[string]string{"log": "runtime logs", "audit": "audit events", "elwisp": "elwisp logs"}[req.Name], d)
		})
	case command.ResultKindUsage:
		return formatCommandData(result.Data, controlformat.Usage)
	case command.ResultKindDoctor:
		return formatCommandData(result.Data, controlformat.Doctor)
	}
	return ""
}

// JSON 传输保持原始结构；这里只为文本出口解码，不修改 Result.Data。
func formatCommandData[T any](data any, render func(T) string) string {
	if data == nil {
		return ""
	}
	if value, ok := data.(T); ok {
		return render(value)
	}
	if raw, ok := data.(json.RawMessage); ok {
		var value T
		if err := json.Unmarshal(raw, &value); err != nil {
			return "读取命令结果失败: " + err.Error()
		}
		return render(value)
	}
	return fmt.Sprintf("无法展示命令结果: %T", data)
}
func commandUsage(req command.Request, name, args string) string {
	text := req.Prefix + name
	if args != "" {
		text += " " + args
	}
	return text
}
