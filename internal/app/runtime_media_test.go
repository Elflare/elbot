package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"elbot/internal/config"
)

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
