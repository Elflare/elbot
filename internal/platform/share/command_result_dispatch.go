package share

import command "elbot/internal/command/share"

func FormatCommandResult(result *command.Result) string {
	if result == nil {
		return ""
	}
	if text := FormatSimpleCommandResult(result); text != "" {
		return text
	}
	switch data := result.Data.(type) {
	case command.SessionChangeData:
		return formatSessionChange(data)
	case *command.SessionChangeData:
		if data != nil {
			return formatSessionChange(*data)
		}
	case command.SessionsData:
		return formatSessions(data)
	case *command.SessionsData:
		if data != nil {
			return formatSessions(*data)
		}
	case command.MessagesData:
		return formatMessages(data)
	case *command.MessagesData:
		if data != nil {
			return formatMessages(*data)
		}
	case command.StatusData:
		return formatStatus(data)
	case *command.StatusData:
		if data != nil {
			return formatStatus(*data)
		}
	case command.ToolsData:
		return formatTools(data)
	case command.RollbacksData:
		return formatRollbacks(data)
	case command.RequestsData:
		return formatRequests(data)
	case command.ModelsData:
		return formatModels(data)
	case command.HelpData:
		return formatHelp(data)
	case command.HooksData:
		return formatHooks(data)
	case command.LogsData:
		return formatLogs(data)
	case command.UsageData:
		return formatUsage(data)
	case command.DoctorData:
		return formatDoctor(data)
	}
	return ""
}
