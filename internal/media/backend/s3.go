package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"elbot/internal/media/share"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// S3Options 只描述 S3 后端构造，不携带完整应用或交付配置。
type S3Options struct {
	Endpoint    string                  // 可选的 S3 兼容端点。
	Region      string                  // 请求签名和客户端区域。
	Bucket      string                  // 对象所属桶。
	Credentials aws.CredentialsProvider // 根部按配置解析的凭据来源。
}

// S3Backend 只负责对象传输和签名，不保存媒体记录或签名缓存。
type S3Backend struct {
	client  *s3.Client        // 对象读写客户端。
	presign *s3.PresignClient // URL 签名客户端。
	bucket  string            // 对象所属桶。
}

// NewS3Backend 只使用根部交付的配置与凭据，不加载 AWS 默认配置链。
func NewS3Backend(ctx context.Context, opts S3Options) (*S3Backend, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := s3.Options{Region: opts.Region, Credentials: opts.Credentials}
	if opts.Endpoint != "" {
		options.BaseEndpoint = aws.String(opts.Endpoint)
		options.UsePathStyle = true
	}
	client := s3.New(options)
	return &S3Backend{client: client, presign: s3.NewPresignClient(client), bucket: opts.Bucket}, nil
}

// Put 写入内容寻址对象，返回实际对象键。
// 不可 Seek 的输入同步暂存到系统临时文件，以支持签名、校验和及重试。
// 暂存前后检查取消；原输入由调用方关闭，临时文件由本次调用释放。
func (b *S3Backend) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error) {
	if _, ok := input.(io.ReadSeeker); !ok {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		tmp, err := os.CreateTemp("", ".elbot-media-upload-*")
		if err != nil {
			return "", fmt.Errorf("create media upload temporary file: %w", err)
		}
		tmpPath := tmp.Name()
		defer os.Remove(tmpPath)
		defer tmp.Close()
		if _, err := io.Copy(tmp, input); err != nil {
			return "", fmt.Errorf("prepare media upload: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return "", fmt.Errorf("rewind media upload: %w", err)
		}
		input = tmp
	}
	key := objectKey(id)
	_, err = b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.bucket), Key: aws.String(key), Body: input,
		ContentLength: aws.Int64(size), ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("put media object: %w", err)
	}
	return key, nil
}

// Open 按对象键读取内容，不查询媒体仓储。
func (b *S3Backend) Open(ctx context.Context, location string) (io.ReadCloser, error) {
	output, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(location)})
	if err != nil {
		var remoteErr smithy.APIError
		if errors.As(err, &remoteErr) && (remoteErr.ErrorCode() == "NoSuchKey" || remoteErr.ErrorCode() == "NotFound") {
			return nil, fmt.Errorf("get media object: %w: %w", fs.ErrNotExist, err)
		}
		return nil, fmt.Errorf("get media object: %w", err)
	}
	return output.Body, nil
}

// Remove 删除给定对象键。
func (b *S3Backend) Remove(ctx context.Context, location string) error {
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(location)}); err != nil {
		return fmt.Errorf("delete media object: %w", err)
	}
	return nil
}

// PresignGet 为给定对象键生成指定期限的读取 URL，不处理缓存。
func (b *S3Backend) PresignGet(ctx context.Context, location string, expiry time.Duration) (string, error) {
	request, err := b.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(location)}, func(options *s3.PresignOptions) {
		options.Expires = expiry
	})
	if err != nil {
		return "", fmt.Errorf("presign media object: %w", err)
	}
	return request.URL, nil
}

// objectKey 将内容标识映射为远端对象键。
func objectKey(id string) string {
	return "media/" + id[len(share.IDPrefix):]
}

var _ share.RemoteBackend = (*S3Backend)(nil)
