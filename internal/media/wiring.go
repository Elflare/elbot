package media

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"elbot/internal/config"
	"elbot/internal/events"
	"elbot/internal/media/backend"
	"elbot/internal/media/object"
	"elbot/internal/media/share"
	"elbot/internal/media/transfer"
	"elbot/internal/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
)

// NewMediaCenter 接收完整配置，在根部拆分对象、交付和后端配置并装配。
// 本地对象目录沿用沙盒根目录同级的 media；凭据解析归根部，构造远端客户端不发起网络请求。
// 构造负责中断恢复和首次历史对账，不接管借用存储。
func NewMediaCenter(ctx context.Context, cfg *config.Config, deps MediaCenterDeps) (*MediaCenter, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deliveryConfig := cfg.FileDelivery
	objectBackend := "local"
	var remote share.RemoteBackend
	switch deliveryConfig.Backend {
	case "base64":
	case "hybrid", "s3":
		if deliveryConfig.Backend == "s3" {
			objectBackend = "s3"
		}
		provider, err := resolveFileDeliveryCredentials(deliveryConfig, filepath.Dir(cfg.ConfigPath))
		if err != nil {
			return nil, fmt.Errorf("media credentials: %w", err)
		}
		remote = backend.NewS3Backend(backend.S3Options{
			Endpoint: deliveryConfig.S3Endpoint, Region: deliveryConfig.S3Region,
			Bucket: deliveryConfig.S3Bucket, Credentials: provider,
		})
	default:
		return nil, fmt.Errorf("unsupported media backend %q", deliveryConfig.Backend)
	}
	local := &backend.LocalBackend{Root: filepath.Join(filepath.Dir(cfg.Sandbox.Root), "media")}
	objects, err := object.NewManager(ctx, object.Options{
		Backend: objectBackend, MaxImportBytes: cfg.PlatformFiles.MaxReceiveFileBytes,
		DownloadTimeout: time.Duration(cfg.PlatformFiles.DownloadTimeoutSecs) * time.Second, Media: cfg.Media, Now: storage.Now,
	}, object.Deps{
		Media: deps.Store, References: deps.Store, Local: local, Remote: remote,
	})
	if err != nil {
		return nil, fmt.Errorf("media objects: %w", err)
	}
	if err := objects.ReconcileHistory(ctx, deps.ChatHistory); err != nil {
		return nil, fmt.Errorf("reconcile history media: %w", err)
	}
	m := &MediaCenter{objects: objects, chatHistory: deps.ChatHistory}
	m.transfer = transfer.NewManager(objects, transfer.Options{
		Log:     events.Log,
		Backend: deliveryConfig.Backend, MaxDirectBase64Bytes: deliveryConfig.MaxDirectBase64Bytes,
		MaxImportBytes: cfg.PlatformFiles.MaxReceiveFileBytes,
	})
	if err := m.connectUsageFinished(); err != nil {
		return nil, err
	}
	return m, nil
}

// resolveFileDeliveryCredentials 解析远端凭据，错误不包含凭据值。
func resolveFileDeliveryCredentials(cfg config.FileDeliveryConfig, configDir string) (aws.CredentialsProvider, error) {
	resolve := func(name, label string) (string, error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("%s environment variable name is empty", label)
		}
		value, ok, err := config.ConfigEnv(name, configDir)
		if err != nil {
			return "", fmt.Errorf("resolve %s environment variable %q: %w", label, name, err)
		}
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
