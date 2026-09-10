package media

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"elbot/internal/config"
	"elbot/internal/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type S3Backend struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
}

func NewS3Backend(ctx context.Context, cfg config.FileDeliveryConfig, provider aws.CredentialsProvider) (*S3Backend, error) {
	if provider == nil {
		return nil, fmt.Errorf("s3 credentials are not configured")
	}
	value, err := provider.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve s3 credentials: %w", err)
	}
	if strings.TrimSpace(value.AccessKeyID) == "" || strings.TrimSpace(value.SecretAccessKey) == "" {
		return nil, fmt.Errorf("s3 credentials are not configured")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.S3Region),
		awsconfig.WithCredentialsProvider(provider),
	}
	if cfg.S3Endpoint != "" {
		loadOptions = append(loadOptions, awsconfig.WithBaseEndpoint(cfg.S3Endpoint))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load s3 config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		options.UsePathStyle = cfg.S3Endpoint != ""
	})
	return &S3Backend{client: client, presign: s3.NewPresignClient(client), bucket: cfg.S3Bucket}, nil
}

func (b *S3Backend) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (string, error) {
	key := objectKey(id)
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key), Body: input, ContentLength: aws.Int64(size), ContentType: aws.String(contentType)})
	if err != nil {
		return "", fmt.Errorf("put media object: %w", err)
	}
	return key, nil
}

func (b *S3Backend) Open(ctx context.Context, media *storage.Media) (io.ReadCloser, error) {
	key := media.ObjectKey
	if key == "" {
		key = objectKey(media.ID)
	}
	output, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("get media object: %w", err)
	}
	return output.Body, nil
}

func (b *S3Backend) Remove(ctx context.Context, media *storage.Media) error {
	key := media.ObjectKey
	if key == "" {
		key = objectKey(media.ID)
	}
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("delete media object: %w", err)
	}
	return nil
}

func (b *S3Backend) PresignGet(ctx context.Context, media *storage.Media, expiry time.Duration) (string, error) {
	key := media.ObjectKey
	if key == "" {
		key = objectKey(media.ID)
	}
	request, err := b.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(key)}, func(options *s3.PresignOptions) {
		options.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("presign media object: %w", err)
	}
	return request.URL, nil
}

func objectKey(id string) string {
	return "media/" + strings.TrimPrefix(id, IDPrefix)
}
