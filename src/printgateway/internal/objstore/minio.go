// Package objstore adapts the shared github.com/LabOS-co/go-packages/
// cloud_storage package to printgw.ObjectStore. All the actual S3/MinIO
// logic (auth, presigning, streaming, 404 classification) lives in
// cloud_storage — this file is deliberately thin, the same role cups.go and
// fetch.go play for Submitter/Fetcher: adapt a shared/stdlib dependency to
// this service's own narrow port, so printgw itself never imports minio-go
// (or cloud_storage) directly.
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
	client cloud_storage.CloudStorageStreamingClient
}

// New builds a MinIO-backed ObjectStore. url/bucket/accessKey/secretKey are
// required (mirroring cloud_storage.NewS3's own validation); region may be
// empty, but see cloud_storage.CloudStorageSettings.Region's doc comment —
// leaving it empty costs a live network round trip and, on a non-AWS
// backend whose bucket-location lookup fails, can silently sign requests
// for the wrong region.
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
// as *apperr.HTTPError{Status: 404}, translated from cloud_storage.ErrNotFound
// so callers never need to know this is backed by MinIO specifically.
//
// The returned io.ReadCloser is cloud_storage.GetObject's CloudStorageObject
// value, which is bound to ctx for its entire read lifetime — a Read after
// ctx is done fails with ctx's error, not just this call.
func (m *MinIO) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	object, size, err := m.client.GetObject(ctx, key)
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
func (m *MinIO) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	url, err := m.client.PresignGetURL(ctx, key, ttl)
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
func (m *MinIO) PresignPut(ctx context.Context, key string, ttl time.Duration) (string, error) {
	url, err := m.client.PresignPutURL(ctx, key, ttl)
	if err != nil {
		return "", &apperr.HTTPError{
			Status:   http.StatusBadGateway,
			Public:   "failed to presign object URL",
			Internal: err,
		}
	}
	return url, nil
}
