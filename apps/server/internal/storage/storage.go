// Package storage is the S3-compatible object store behind file assets
// (Plane's S3Storage). Production uses Cloudflare R2; any S3-compatible
// object storage works, since addressing is path-style.
//
// R2 has no presigned POST, so uploads don't go to the bucket directly: the
// API hands the browser a POST policy addressed to itself (see post.go),
// checks it the way S3 would, and writes the file with PutObject. Downloads
// still redirect to presigned GET URLs, which R2 supports.
package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

// Config is the bucket and its credentials (AWS_* variables, as Django
// names them).
type Config struct {
	Endpoint        string // AWS_S3_ENDPOINT_URL, e.g. https://<account>.r2.cloudflarestorage.com
	AccessKeyID     string // AWS_ACCESS_KEY_ID
	SecretAccessKey string // AWS_SECRET_ACCESS_KEY
	Bucket          string // AWS_S3_BUCKET_NAME
	Region          string // AWS_REGION; R2 wants "auto"
	// SignedURLExpiration is how long presigned URLs and upload policies
	// stay valid (SIGNED_URL_EXPIRATION).
	SignedURLExpiration time.Duration
}

// Configured reports whether enough is set to reach a bucket.
func (c Config) Configured() bool {
	return c.Endpoint != "" && c.AccessKeyID != "" && c.SecretAccessKey != "" && c.Bucket != ""
}

// ErrNotConfigured is returned by every operation when no bucket is set up.
var ErrNotConfigured = errors.New("storage: file storage is not configured (AWS_S3_ENDPOINT_URL, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_S3_BUCKET_NAME)")

// ErrNotFound is a missing object.
var ErrNotFound = errors.New("storage: object not found")

// IsClientError reports whether err is an error answer from the bucket
// (botocore's ClientError), as opposed to not reaching it.
func IsClientError(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr)
}

// Client is one bucket. A nil *Client is valid and fails every call with
// ErrNotConfigured, so the server runs without storage.
type Client struct {
	cfg     Config
	s3      *s3.Client
	presign *s3.PresignClient
}

// New returns a client for cfg, or nil when cfg isn't configured.
func New(cfg Config) *Client {
	if !cfg.Configured() {
		return nil
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}
	if cfg.SignedURLExpiration <= 0 {
		cfg.SignedURLExpiration = time.Hour
	}
	creds := aws.Credentials{AccessKeyID: cfg.AccessKeyID, SecretAccessKey: cfg.SecretAccessKey, Source: "plane-lite"}
	client := s3.New(s3.Options{
		Region: cfg.Region,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return creds, nil
		}),
		BaseEndpoint: aws.String(strings.TrimRight(cfg.Endpoint, "/")),
		UsePathStyle: true,
		// R2 rejects the SDK's default CRC checksums on some operations;
		// only send them where S3 requires them.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
		HTTPClient:                 &http.Client{Timeout: 5 * time.Minute},
	})
	return &Client{cfg: cfg, s3: client, presign: s3.NewPresignClient(client)}
}

// Bucket is the bucket name.
func (c *Client) Bucket() string {
	if c == nil {
		return ""
	}
	return c.cfg.Bucket
}

// Expiration is the lifetime of presigned URLs and upload policies.
func (c *Client) Expiration() time.Duration {
	if c == nil {
		return time.Hour
	}
	return c.cfg.SignedURLExpiration
}

// PresignGet is S3Storage.generate_presigned_url: a GET URL for key that
// makes the bucket answer with the given Content-Disposition.
func (c *Client) PresignGet(ctx context.Context, key, contentDisposition string) (string, error) {
	if c == nil {
		return "", ErrNotConfigured
	}
	req, err := c.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket:                     aws.String(c.cfg.Bucket),
		Key:                        aws.String(key),
		ResponseContentDisposition: aws.String(contentDisposition),
	}, s3.WithPresignExpires(c.cfg.SignedURLExpiration))
	if err != nil {
		return "", fmt.Errorf("storage: presign %q: %w", key, err)
	}
	return req.URL, nil
}

// Put writes an object. body must be seekable so the request can be signed
// over its content.
func (c *Client) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, contentType string) error {
	if c == nil {
		return ErrNotConfigured
	}
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.cfg.Bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	return nil
}

// Metadata is what S3Storage.get_object_metadata keeps of a HEAD.
type Metadata struct {
	ContentType   *string
	ContentLength int64
	LastModified  *time.Time
	ETag          *string
	Metadata      map[string]string
}

// Head is a HEAD of key; ErrNotFound when it doesn't exist.
func (c *Client) Head(ctx context.Context, key string) (*Metadata, error) {
	if c == nil {
		return nil, ErrNotConfigured
	}
	out, err := c.s3.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(c.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NotFound" || apiErr.ErrorCode() == "NoSuchKey") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("storage: head %q: %w", key, err)
	}
	m := &Metadata{ContentType: out.ContentType, LastModified: out.LastModified, ETag: out.ETag, Metadata: out.Metadata}
	if out.ContentLength != nil {
		m.ContentLength = *out.ContentLength
	}
	if m.Metadata == nil {
		m.Metadata = map[string]string{}
	}
	return m, nil
}

// Copy is S3Storage.copy_object: a server-side copy within the bucket.
func (c *Client) Copy(ctx context.Context, src, dst string) error {
	if c == nil {
		return ErrNotConfigured
	}
	segs := strings.Split(c.cfg.Bucket+"/"+src, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	_, err := c.s3.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(c.cfg.Bucket),
		CopySource: aws.String(strings.Join(segs, "/")),
		Key:        aws.String(dst),
	})
	if err != nil {
		return fmt.Errorf("storage: copy %q to %q: %w", src, dst, err)
	}
	return nil
}
