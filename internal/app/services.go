package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"elbot/internal/config"
	"elbot/internal/contextinfo"
	"elbot/internal/doctor"
	globalevents "elbot/internal/events"
	hookrules "elbot/internal/hook/rules"
	"elbot/internal/media"
	"elbot/internal/modelmgr"
	platformbuiltin "elbot/internal/platform/builtin"
	"elbot/internal/security"
)

// sharedServices owns only infrastructure shared across business lines.
type sharedServices struct {
	Models *modelmgr.Service
	Policy *security.Policy
	Media  *media.Manager
	Doctor *doctor.Service
}

func buildSharedServices(ctx context.Context, req WiringRequest) (*sharedServices, error) {
	foundation, cfg := req.Foundation, req.Foundation.Config
	models, err := modelmgr.New(modelmgr.Options{
		Clients: req.Models.ByProvider, Providers: cfg.Providers, ModeModels: cfg.ModeModels,
		CompactModel: cfg.CompactModel, NamingModel: cfg.NamingModel,
		StatePath: cfg.StateConfigPath, DefaultMode: cfg.Session.DefaultMode,
		Metadata: cfg.ModelMetadata,
		LoadProviders: func(ctx context.Context) (config.ProviderFile, error) {
			if err := ctx.Err(); err != nil {
				return config.ProviderFile{}, err
			}
			return config.LoadProviders(cfg.ProvidersConfigPath, filepath.Dir(cfg.ConfigPath))
		},
		BuildClient: req.Models.BuildClient,
	})
	if err != nil {
		return nil, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = models.Close()
		}
	}()
	credentials, err := resolveFileDeliveryCredentials(cfg.FileDelivery, filepath.Dir(cfg.ConfigPath))
	if err != nil {
		{
			record := globalevents.LogRecord{
				Category:     globalevents.LogRuntime,
				Level:        slog.LevelWarn,
				EventName:    "media_backend_unavailable",
				Module:       "app",
				Type:         globalevents.ContentText,
				Content:      fmt.Sprintf("S3 media backend is unavailable; remote operations will fail until configuration is fixed\nerror=%v", err),
				ContentLimit: 4096,
				ResultStatus: globalevents.ResultFailed,
			}
			if errors.Is(err, context.Canceled) {
				record.ResultStatus = globalevents.ResultCanceled
				record.Level = slog.LevelInfo
			}
			facts, _ := contextinfo.ExecutionFromContext(ctx)
			if facts.SessionID != "" {
				record.Identity.SessionID = &facts.SessionID
			}
			if facts.RunID != "" {
				record.Identity.RunID = &facts.RunID
			}
			if facts.Attempt != "" {
				record.Identity.Attempt = &facts.Attempt
			}
			if facts.RequestID != "" {
				record.Identity.RequestID = &facts.RequestID
			}
			if facts.RootRequestID != "" {
				record.Identity.RootRequestID = &facts.RootRequestID
			}
			_ = globalevents.EmitLog(ctx, record)
		}
		credentials = nil
	}
	mediaCenter, err := media.NewConfigured(ctx, foundation.Store, filepath.Join(filepath.Dir(cfg.Sandbox.Root), "media"), cfg.FileDelivery, credentials)
	if err != nil {
		return nil, err
	}
	if err := foundation.Store.Media().RecoverInterrupted(ctx); err != nil {
		return nil, err
	}
	mediaCenter.History = foundation.ChatHistory
	if err := mediaCenter.ReconcileHistory(ctx); err != nil {
		return nil, err
	}
	mediaCenter.MaxImportBytes = cfg.PlatformFiles.MaxReceiveFileBytes
	mediaCenter.DownloadTimeout = time.Duration(cfg.PlatformFiles.DownloadTimeoutSecs) * time.Second
	mediaCenter.Media = cfg.Media

	definitions := append(platformbuiltin.ConfigDefinitions(), hookrules.ConfigDefinition())
	diagnostics, err := doctor.New(cfg.ConfigPath, config.NewInspector(definitions...))
	if err != nil {
		return nil, err
	}
	s := &sharedServices{Models: models, Policy: security.NewPolicy(cfg.Security.UserMaxToolRisk, cfg.Security.SuperadminConfirmRisk, cfg.Security.Superadmins), Media: mediaCenter, Doctor: diagnostics}
	complete = true
	return s, nil
}

func resolveFileDeliveryCredentials(cfg config.FileDeliveryConfig, configDir string) (aws.CredentialsProvider, error) {
	backend := strings.TrimSpace(cfg.Backend)
	if backend == "" {
		backend = config.Default().FileDelivery.Backend
	}
	if backend != "s3" && backend != "hybrid" {
		return nil, nil
	}
	resolve := func(name, label string) (string, error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("%s environment variable name is empty", label)
		}
		value, ok, err := config.ConfigEnv(name, configDir)
		if err != nil {
			return "", fmt.Errorf("resolve %s environment variable %q: %w", label, name, err)
		}
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			return "", fmt.Errorf("%s environment variable %q is not configured", label, name)
		}
		return value, nil
	}
	accessKey, err := resolve(cfg.S3AccessKeyEnv, "s3 access key")
	if err != nil {
		return nil, err
	}
	secretKey, err := resolve(cfg.S3SecretKeyEnv, "s3 secret key")
	if err != nil {
		return nil, err
	}
	return credentials.NewStaticCredentialsProvider(accessKey, secretKey, ""), nil
}
