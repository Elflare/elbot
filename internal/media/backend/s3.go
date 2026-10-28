package backend

import (
	"context"
	"io"
	"time"

	"elbot/internal/media/share"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
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

// NewS3Backend 根据后端配置构造客户端并检查实际凭据。
func NewS3Backend(ctx context.Context, opts S3Options) (*S3Backend, error) { panic("TODO") }

// Put 写入内容寻址对象，返回实际对象键。
func (b *S3Backend) Put(ctx context.Context, id string, input io.Reader, size int64, contentType string) (location string, err error) {
	panic("TODO")
}

// Open 按对象键读取内容，不查询媒体仓储。
func (b *S3Backend) Open(ctx context.Context, location string) (io.ReadCloser, error) { panic("TODO") }

// Remove 删除给定对象键。
func (b *S3Backend) Remove(ctx context.Context, location string) error { panic("TODO") }

// PresignGet 为给定对象键生成指定期限的读取 URL，不处理缓存。
func (b *S3Backend) PresignGet(ctx context.Context, location string, expiry time.Duration) (string, error) {
	panic("TODO")
}

// objectKey 将内容标识映射为远端对象键。
func objectKey(id string) string { panic("TODO") }

var _ share.RemoteBackend = (*S3Backend)(nil)
