// Package objstore adapts github.com/LabOS-co/go-packages/cloud_storage to
// printgw.ObjectStore, so printgw itself never imports cloud_storage/minio-go
// directly.
package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/LabOS-co/go-packages/cloud_storage"
	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/apperr"
)

// MinIO implements printgw.ObjectStore over cloud_storage.NewS3.
type MinIO struct {
	client cloud_storage.CloudStorageClient
}

// New builds a MinIO-backed ObjectStore. url/bucket/accessKey/secretKey are
// required; region may be empty, at the cost of an extra bucket-location
// lookup (see cloud_storage.CloudStorageSettings.Region).
func New(url, bucket, accessKey, secretKey, region string, insecure bool, logger logs.Logger, meta *logs.LogMetaData) (*MinIO, error) {
	client, err := cloud_storage.NewS3(&cloud_storage.CloudStorageSettings{
		Url:        url,
		BucketName: bucket,
		Credentials: &cloud_storage.CloudStorageCredentials{
			Id:     accessKey,
			Secret: secretKey,
		},
		Logger:      logger,
		LogMetaData: meta,
		Insecure:    insecure,
		Region:      region,
	})
	if err != nil {
		return nil, err
	}
	return &MinIO{client: client}, nil
}

// Get returns key's content and its exact size. A missing key is reported
// as *apperr.HTTPError{Status: 404}. cloud_storage.CloudStorageClient's
// GetDownloadObject has no ctx of its own, so ctx is not honored mid-read -
// only checked here before starting the download.
func (m *MinIO) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	object, size, err := m.client.GetDownloadObject(key)
	if err != nil {
		if errors.Is(err, cloud_storage.ErrNotFound) {
			return nil, 0, &apperr.HTTPError{
				Status:   http.StatusNotFound,
				Public:   fmt.Sprintf("object %q not found", key),
				Internal: err,
			}
		}
		return nil, 0, &apperr.HTTPError{
			Status:   http.StatusBadGateway,
			Public:   "failed to fetch object from storage",
			Internal: err,
		}
	}
	return object, size, nil
}

// PresignGet returns a time-limited URL a third party can GET directly.
// cloud_storage.CloudStorageClient's PresignGetURL has no ctx of its own.
func (m *MinIO) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	url, err := m.client.PresignGetURL(key, ttl)
	if err != nil {
		return "", &apperr.HTTPError{
			Status:   http.StatusBadGateway,
			Public:   "failed to presign object URL",
			Internal: err,
		}
	}
	return url, nil
}

// PresignPut returns a time-limited URL a third party can PUT directly.
// cloud_storage.CloudStorageClient's PresignPutURL has no ctx of its own.
func (m *MinIO) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	url, err := m.client.PresignPutURL(key, ttl)
	if err != nil {
		return "", &apperr.HTTPError{
			Status:   http.StatusBadGateway,
			Public:   "failed to presign object URL",
			Internal: err,
		}
	}
	return url, nil
}
