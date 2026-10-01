package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 stores objects in one bucket of a store that speaks S3.
type S3 struct {
	client *minio.Client
	bucket string
}

type S3Options struct {
	Endpoint  string // http(s)://host:port
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	// Addressing is "path", "virtual" or "auto"; "" is "auto".
	Addressing string
}

// NewS3 connects to the store and creates the bucket if it isn't there.
func NewS3(ctx context.Context, o S3Options) (*S3, error) {
	u, err := url.Parse(o.Endpoint)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("PAIL_S3_ENDPOINT %q is not a URL like http://versitygw:7070", o.Endpoint)
	}
	// Left to itself, the client puts the bucket in front of the host for
	// the stores known to want it there, and after the host for the rest.
	lookup := minio.BucketLookupAuto
	switch o.Addressing {
	case "path":
		lookup = minio.BucketLookupPath
	case "virtual":
		lookup = minio.BucketLookupDNS
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure:       u.Scheme == "https",
		Region:       o.Region,
		BucketLookup: lookup,
	})
	if err != nil {
		return nil, err
	}

	ok, err := client.BucketExists(ctx, o.Bucket)
	if err != nil {
		return nil, fmt.Errorf("can't reach the bucket %q at %s. Check PAIL_S3_ENDPOINT, the keys, PAIL_S3_REGION and PAIL_S3_BUCKET: %w", o.Bucket, o.Endpoint, err)
	}
	if !ok {
		if err := client.MakeBucket(ctx, o.Bucket, minio.MakeBucketOptions{Region: o.Region}); err != nil {
			return nil, fmt.Errorf("there is no bucket %q at %s, and these keys can't make it. Make it there, or name another with PAIL_S3_BUCKET: %w", o.Bucket, o.Endpoint, err)
		}
	}
	return &S3{client: client, bucket: o.Bucket}, nil
}

func (s *S3) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, body, size, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (s *S3) Read(ctx context.Context, key string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	b, err := io.ReadAll(obj)
	if err != nil {
		if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

func (s *S3) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	return s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
}

func (s *S3) Copy(ctx context.Context, src, dst string) error {
	_, err := s.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: s.bucket, Object: dst},
		minio.CopySrcOptions{Bucket: s.bucket, Object: src})
	return err
}

func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		keys = append(keys, obj.Key)
	}
	return keys, nil
}

func (s *S3) DeletePrefix(ctx context.Context, prefix string) error {
	keys, err := s.List(ctx, prefix)
	if err != nil {
		return err
	}
	for _, key := range keys {
		if err := s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}
