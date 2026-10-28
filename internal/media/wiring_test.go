package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"elbot/internal/config"
)

func TestResolveFileDeliveryCredentials(t *testing.T) {
	const accessName = "ELBOT_TEST_MEDIA_ACCESS"
	const secretName = "ELBOT_TEST_MEDIA_SECRET"
	for _, mode := range []string{"s3", "hybrid"} {
		for _, process := range []bool{false, true} {
			t.Run(mode+"/process="+map[bool]string{false: "false", true: "true"}[process], func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv(accessName, "")
				t.Setenv(secretName, "")
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(accessName+"=dotenv-access\n"+secretName+"=dotenv-secret\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				wantAccess, wantSecret := "dotenv-access", "dotenv-secret"
				if process {
					wantAccess, wantSecret = "process-access", "process-secret"
					t.Setenv(accessName, wantAccess)
					t.Setenv(secretName, wantSecret)
				}
				provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{
					Backend: mode, S3AccessKeyEnv: accessName, S3SecretKeyEnv: secretName,
				}, dir)
				if err != nil {
					t.Fatal(err)
				}
				value, err := provider.Retrieve(t.Context())
				if err != nil || value.AccessKeyID != wantAccess || value.SecretAccessKey != wantSecret {
					t.Fatalf("unexpected credentials or retrieval error: %v", err)
				}
			})
		}
	}
}

func TestResolveFileDeliveryCredentialsFailures(t *testing.T) {
	const accessName = "ELBOT_TEST_MEDIA_MISSING_ACCESS"
	const secretName = "ELBOT_TEST_MEDIA_MISSING_SECRET"
	for _, tc := range []struct {
		name, accessEnv, secretEnv, access, secret, want string
	}{
		{"empty access name", "", secretName, "", "", "access key environment variable name is empty"},
		{"empty secret name", accessName, "", "private-access", "", "secret key environment variable name is empty"},
		{"missing access", accessName, secretName, "", "private-secret", accessName},
		{"missing secret", accessName, secretName, "private-access", "", secretName},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(accessName, tc.access)
			t.Setenv(secretName, tc.secret)
			provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{
				Backend: "s3", S3AccessKeyEnv: tc.accessEnv, S3SecretKeyEnv: tc.secretEnv,
			}, t.TempDir())
			if provider != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected credential configuration failure: %v", err)
			}
			if strings.Contains(err.Error(), "private-access") || strings.Contains(err.Error(), "private-secret") {
				t.Fatal("error leaked credentials")
			}
		})
	}
}

func TestResolveFileDeliveryCredentialsSkipsS3ForBase64(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := resolveFileDeliveryCredentials(config.FileDeliveryConfig{Backend: "base64"}, file)
	if err != nil || provider != nil {
		t.Fatalf("base64 tried resolving credentials: %v", err)
	}
	t.Setenv("ELBOT_TEST_MEDIA_BAD_DIR", "")
	_, err = resolveFileDeliveryCredentials(config.FileDeliveryConfig{
		Backend: "s3", S3AccessKeyEnv: "ELBOT_TEST_MEDIA_BAD_DIR",
	}, file)
	if err == nil {
		t.Fatal("remote mode did not report dotenv read error")
	}
}

func TestNewMediaCenterEarlyFailure(t *testing.T) {
	for _, mode := range []string{"canceled", "unknown", "s3", "hybrid"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config.Default()
			cfg.FileDelivery.Backend = mode
			cfg.FileDelivery.S3AccessKeyEnv = ""
			before := *cfg
			ctx := t.Context()
			if mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			// nil Store and real TODO constructors make accidental downstream work fail.
			center, err := NewMediaCenter(ctx, cfg, nil)
			if err == nil || center == nil || center.objects != nil || center.transfer != nil || center.History != nil {
				t.Fatalf("unexpected early construction result: %v, %v", center, err)
			}
			if mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost cancellation: %v", err)
			}
			if mode == "unknown" && !strings.Contains(err.Error(), "unsupported media backend") {
				t.Fatalf("unexpected mode error: %v", err)
			}
			if (mode == "s3" || mode == "hybrid") && !strings.Contains(err.Error(), "media credentials") {
				t.Fatalf("unexpected credential error: %v", err)
			}
			if !reflect.DeepEqual(*cfg, before) {
				t.Fatal("constructor changed caller configuration")
			}
		})
	}
}
