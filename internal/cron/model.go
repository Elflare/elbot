package cron

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"elbot/internal/background"
	"elbot/internal/llm"
	"elbot/internal/security"
	"elbot/internal/storage"
)

type ScheduleMode string

const (
	ScheduleOnce ScheduleMode = "once"
	ScheduleCron ScheduleMode = "cron"
)

type TriggerMode string

const (
	TriggerDirect TriggerMode = "direct"
	TriggerLLM    TriggerMode = "llm"
)

type Metadata struct {
	Kind      string          `json:"kind"`
	Version   int             `json:"version"`
	Title     string          `json:"title"`
	CreatedBy CronActor       `json:"created_by"`
	Schedule  CronSchedule    `json:"schedule"`
	Trigger   CronTrigger     `json:"trigger"`
	Target    CronTarget      `json:"target"`
	LLM       CronLLMMetadata `json:"llm,omitempty"`
}

type CronActor struct {
	ActorID        string `json:"actor_id"`
	Platform       string `json:"platform"`
	PlatformUserID string `json:"platform_user_id"`
	DisplayName    string `json:"display_name,omitempty"`
}

type CronSchedule struct {
	Mode     ScheduleMode `json:"mode"`
	RunAt    string       `json:"run_at,omitempty"`
	CronExpr string       `json:"cron_expr,omitempty"`
}

type CronTrigger struct {
	Mode    TriggerMode `json:"mode"`
	Message string      `json:"message"`
}

type CronTarget struct {
	AllEnabledPlatforms bool   `json:"all_enabled_platforms"`
	SourcePlatform      string `json:"source_platform"`
}

type CronLLMMetadata struct {
	ToolListNames []string `json:"tool_list_names,omitempty"`
	SessionMode   string   `json:"session_mode,omitempty"`
}

type DeliveryStatus string

const (
	DeliveryPending           DeliveryStatus = "pending"
	DeliveryDelivered         DeliveryStatus = "delivered"
	DeliveryFallbackPending   DeliveryStatus = "fallback_pending"
	DeliveryFallbackDelivered DeliveryStatus = "fallback_delivered"
)

type CronDeliveryState struct {
	RunID           string                    `json:"run_id"`
	ReportReady     bool                      `json:"report_ready,omitempty"`
	TaskCompleted   bool                      `json:"task_completed,omitempty"`
	Report          string                    `json:"report,omitempty"`
	ReportSegments  []llm.MessageSegment      `json:"report_segments,omitempty"`
	ReportSessionID string                    `json:"report_session_id,omitempty"`
	ReportMessageID string                    `json:"report_message_id,omitempty"`
	Targets         []CronDeliveryTargetState `json:"targets,omitempty"`
}

type CronDeliveryTargetState struct {
	Key     string                    `json:"key"`
	Outputs []CronDeliveryOutputState `json:"outputs,omitempty"`
}

type CronDeliveryOutputState struct {
	ID           string         `json:"id"`
	Status       DeliveryStatus `json:"status"`
	FallbackText string         `json:"fallback_text,omitempty"`
}

type UpsertRequest struct {
	Name                string
	Title               string
	ScheduleMode        ScheduleMode
	RunAt               string
	CronExpr            string
	TriggerMode         TriggerMode
	Message             string
	ToolListNames       []string
	SessionMode         string
	AllEnabledPlatforms bool

	Enabled        bool
	Actor          security.Actor
	SourcePlatform string
}

type PatchRequest struct {
	Name                string
	Title               *string
	ScheduleMode        *ScheduleMode
	RunAt               *string
	CronExpr            *string
	TriggerMode         *TriggerMode
	Message             *string
	ToolListNames       *[]string
	SessionMode         *string
	AllEnabledPlatforms *bool

	Enabled *bool
	Actor   security.Actor
}

type JobView struct {
	Job      storage.CronJob   `json:"job"`
	Metadata Metadata          `json:"metadata"`
	Delivery CronDeliveryState `json:"delivery,omitempty"`
}

type CronLLMResult = background.JSONResult

func decodeMetadata(raw string) (Metadata, error) {
	var meta Metadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return Metadata{}, err
	}
	if meta.Kind != metadataKind {
		return Metadata{}, fmt.Errorf("unsupported cron metadata kind %q", meta.Kind)
	}
	return meta, nil
}

func validateMetadata(meta Metadata) error {
	if strings.TrimSpace(meta.Trigger.Message) == "" {
		return fmt.Errorf("message is required")
	}
	if meta.Trigger.Mode != TriggerDirect && meta.Trigger.Mode != TriggerLLM {
		return fmt.Errorf("unsupported trigger mode %q", meta.Trigger.Mode)
	}
	if meta.Target.SourcePlatform == "" {
		return fmt.Errorf("source platform is required")
	}
	_, err := scheduleExpr(meta.Schedule)
	return err
}

func scheduleExpr(schedule CronSchedule) (string, error) {
	switch schedule.Mode {
	case ScheduleOnce:
		runAt, err := parseRunAt(schedule.RunAt)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d %d %d %d *", runAt.Minute(), runAt.Hour(), runAt.Day(), int(runAt.Month())), nil
	case ScheduleCron:
		expr := strings.TrimSpace(schedule.CronExpr)
		if expr == "" {
			return "", fmt.Errorf("cron_expr is required")
		}
		if len(strings.Fields(expr)) != 5 {
			return "", fmt.Errorf("cron_expr must be a 5-field cron expression")
		}
		return expr, nil
	default:
		return "", fmt.Errorf("unsupported schedule mode %q", schedule.Mode)
	}
}

func parseRunAt(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("run_at is required")
	}
	runAt, err := time.ParseInLocation(timeLayout, value, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("run_at must use YYYY-MM-DD HH:MM:SS: %w", err)
	}
	return runAt, nil
}

func normalizeJobName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "user.cron.")
	name = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if name == "" {
		return ""
	}
	return "user.cron." + name
}

func cronSandboxSubdir(jobName string) string {
	name := strings.TrimPrefix(normalizeJobName(jobName), "user.cron.")
	name = regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "_-")
	if name == "" {
		name = "default"
	}
	return filepath.ToSlash(filepath.Join("cron", name))
}

func cronScopeID(jobName string) string { return "cron:" + normalizeJobName(jobName) }

func cronSessionMetadata(jobName, sourceSessionID string, copied bool) string {
	data, _ := json.Marshal(map[string]any{"title_renamed": true, "title_source": "cron", "cron_job_name": jobName, "cron_source_session_id": sourceSessionID, "cron_broadcast_copy": copied})
	return string(data)
}

func actorMetadata(actor security.Actor) CronActor {
	return CronActor{ActorID: actor.ID, Platform: actor.Platform, PlatformUserID: actor.PlatformUserID, DisplayName: actor.DisplayName}
}

func requireSuperadmin(actor security.Actor) error {
	if actor.Role != security.RoleSuperadmin {
		return fmt.Errorf("cron requires superadmin role")
	}
	return nil
}

func normalizeToolListNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func validateLLMSessionMode(mode string) (string, error) {
	mode = strings.TrimSpace(mode)
	switch mode {
	case "":
		return "", nil
	case storage.SessionModeWork, storage.SessionModeChat:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported session_mode %q", mode)
	}
}

func normalizeLLMSessionMode(mode string) string {
	mode, err := validateLLMSessionMode(mode)
	if err != nil || mode == "" {
		return ""
	}
	return mode
}
