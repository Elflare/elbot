package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/config"
	"elbot/internal/storage"
)

type startupRecoveryStore struct {
	storage.Store
	media  storage.MediaRepository
	events storage.ElnisEventRepository
}

func (s startupRecoveryStore) Media() storage.MediaRepository            { return s.media }
func (s startupRecoveryStore) ElnisEvents() storage.ElnisEventRepository { return s.events }

type startupRecoveryMedia struct {
	storage.MediaRepository
	before func() error
}

func (r startupRecoveryMedia) RecoverInterrupted(ctx context.Context) error {
	if err := r.before(); err != nil {
		return err
	}
	return r.MediaRepository.RecoverInterrupted(ctx)
}

type startupRecoveryEvents struct {
	storage.ElnisEventRepository
	before func() error
}

func (r startupRecoveryEvents) FailInterrupted(ctx context.Context, from []string, failed, reason string) error {
	if err := r.before(); err != nil {
		return err
	}
	return r.ElnisEventRepository.FailInterrupted(ctx, from, failed, reason)
}

func TestSharedServicesStartupRecovery(t *testing.T) {
	for _, failAt := range []string{"", "media", "elnis"} {
		t.Run("failure="+failAt, func(t *testing.T) {
			req, _, _ := runtimeAssemblyFixture(t)
			if req.Foundation.Config.Elnis.Enabled {
				t.Fatal("fixture must disable Elnis")
			}
			ctx := t.Context()
			store := req.Foundation.Store
			row, err := store.ElnisEvents().Create(ctx, storage.CreateElnisEventRequest{
				EventKey: "old", ElwispName: "source", SourceID: "old", Status: "queued",
			})
			if err != nil {
				t.Fatal(err)
			}
			var calls []string
			wantErr := errors.New("recovery failed")
			before := func(step string) func() error {
				return func() error {
					calls = append(calls, step)
					if failAt == step {
						return wantErr
					}
					return nil
				}
			}
			req.Foundation.Store = startupRecoveryStore{
				Store:  store,
				media:  startupRecoveryMedia{MediaRepository: store.Media(), before: before("media")},
				events: startupRecoveryEvents{ElnisEventRepository: store.ElnisEvents(), before: before("elnis")},
			}
			services, err := buildSharedServices(ctx, req)
			wantCalls, wantStatus := "media,elnis", "failed"
			if failAt == "" {
				if err != nil || services == nil {
					t.Fatalf("startup = %v, %v", services, err)
				}
			} else {
				if !errors.Is(err, wantErr) || services != nil {
					t.Fatalf("startup must fail before publishing services: %v, %v", services, err)
				}
				wantStatus = "queued"
				if failAt == "media" {
					wantCalls = "media"
				}
			}
			if strings.Join(calls, ",") != wantCalls {
				t.Fatalf("recovery order = %v, want %s", calls, wantCalls)
			}
			row, err = store.ElnisEvents().Get(ctx, row.ID)
			if err != nil || row.Status != wantStatus {
				t.Fatalf("recovered event = %#v, %v", row, err)
			}
		})
	}
}

func TestResolveFileDeliveryCredentialsUsesConfigDotEnv(t *testing.T) {
	for _, mode := range []string{"s3", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			const accessName = "ELBOT_TEST_DOTENV_S3_ACCESS"
			const secretName = "ELBOT_TEST_DOTENV_S3_SECRET"
			t.Setenv(accessName, "")
			t.Setenv(secretName, "")
			contents := accessName + "=dotenv-access\n" + secretName + "=dotenv-secret\n"
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}

			provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{
				Backend: mode, S3AccessKeyEnv: accessName, S3SecretKeyEnv: secretName,
			}, dir)
			if err != nil {
				t.Fatal(err)
			}
			credentials, err := provider.Retrieve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if credentials.AccessKeyID != "dotenv-access" || credentials.SecretAccessKey != "dotenv-secret" {
				t.Fatalf("credentials = %#v", credentials)
			}
		})
	}
}

func TestResolveFileDeliveryCredentialsPrefersProcessEnvironment(t *testing.T) {
	dir := t.TempDir()
	const accessName = "ELBOT_TEST_ENV_S3_ACCESS"
	const secretName = "ELBOT_TEST_ENV_S3_SECRET"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(accessName+"=dotenv-access\n"+secretName+"=dotenv-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(accessName, "process-access")
	t.Setenv(secretName, "process-secret")

	provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{
		Backend: "s3", S3AccessKeyEnv: accessName, S3SecretKeyEnv: secretName,
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := provider.Retrieve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccessKeyID != "process-access" || credentials.SecretAccessKey != "process-secret" {
		t.Fatalf("environment did not win: %#v", credentials)
	}
}

func TestResolveFileDeliveryCredentialsRejectsMissingSecret(t *testing.T) {
	dir := t.TempDir()
	const accessName = "ELBOT_TEST_MISSING_S3_ACCESS"
	const secretName = "ELBOT_TEST_MISSING_S3_SECRET"
	t.Setenv(accessName, "")
	t.Setenv(secretName, "")
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(accessName+"=private-access-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{
		Backend: "s3", S3AccessKeyEnv: accessName, S3SecretKeyEnv: secretName,
	}, dir)
	if err == nil || !strings.Contains(err.Error(), secretName) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "private-access-value") {
		t.Fatalf("error leaked credentials: %v", err)
	}
}

func TestResolveFileDeliveryCredentialsSkipsS3ForBase64(t *testing.T) {
	fileInsteadOfDir := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(fileInsteadOfDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{Backend: "base64"}, fileInsteadOfDir)
	if err != nil || provider != nil {
		t.Fatalf("provider = %v, error = %v", provider, err)
	}
}
