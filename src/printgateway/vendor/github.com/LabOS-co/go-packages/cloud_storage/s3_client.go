package cloud_storage

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/LabOS-co/go-packages/logs"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/samber/lo"
)

type s3Client struct {
	ctx         context.Context
	client      *minio.Client
	bucketName  string
	logger      logs.Logger
	logMetaData *logs.LogMetaData
}

func NewS3(settings *CloudStorageSettings) (CloudStorageStreamingClient, error) {
	if settings == nil {
		return nil, fmt.Errorf("missing settings object. S3 client cannot be initialized")
	}
	// Checked before the Credentials.Id/.Secret reads below, which would
	// otherwise nil-deref on a settings object that skips Credentials
	// entirely, instead of reaching the "missing ... id or secret" error
	// two lines down.
	if settings.Credentials == nil {
		return nil, fmt.Errorf("missing credentials. S3 client cannot be initialized")
	}
	if settings.Url == "" || settings.BucketName == "" || settings.Credentials.Id == "" || settings.Credentials.Secret == "" {
		return nil, fmt.Errorf("missing url, bucketName, id or secret. S3 client cannot be initialized")
	}
	// Checked before the first LogInfo call below, which would otherwise
	// nil-deref on a settings object built without a Logger.
	if settings.Logger == nil {
		return nil, fmt.Errorf("missing logger. S3 client cannot be initialized")
	}

	settings.Logger.LogInfo(fmt.Sprintf(
		"Initializing S3 client with url '%s' and bucket '%s'", settings.Url, settings.BucketName),
		settings.LogMetaData,
	)
	if settings.Insecure {
		// LogInfo, not a silent default: Insecure disables TLS, and a
		// production deployment left this way by accident is a real
		// exposure worth surfacing on every single startup, not just
		// documenting in a struct comment nobody reads at deploy time.
		settings.Logger.LogInfo(fmt.Sprintf(
			"S3 client for '%s' configured with Insecure=true (plain http, no TLS)", settings.Url),
			settings.LogMetaData,
		)
	}

	client, err := minio.New(settings.Url, &minio.Options{
		Creds:  credentials.NewStaticV4(settings.Credentials.Id, settings.Credentials.Secret, ""),
		Secure: !settings.Insecure,
		Region: settings.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to initialize S3 client: %s", err)
	}

	settings.Logger.LogInfo("S3 client initialized successfully", settings.LogMetaData)
	return &s3Client{
		ctx:         context.Background(),
		client:      client,
		bucketName:  settings.BucketName,
		logger:      settings.Logger,
		logMetaData: settings.LogMetaData,
	}, nil
}

func (s3 *s3Client) GetFileNames() ([]string, error) {
	s3.logger.LogInfo("Getting files list from cloud storage", s3.logMetaData)

	return s3.getListObjects(true, "", nil)
}

func (s3 *s3Client) GetFolderNames() ([]string, error) {
	s3.logger.LogInfo("Getting folders list from cloud storage", s3.logMetaData)

	foldersNames, err := s3.getListObjects(false, "", nil)
	if err != nil {
		return nil, err
	}

	return lo.Map(foldersNames, func(folderName string, _ int) string {
		return strings.Trim(folderName, "/")
	}), nil
}

func (s3 *s3Client) GetFileNamesByPrefixAndCondition(prefix string, condition func(name string) bool) ([]string, error) {
	s3.logger.LogInfo(fmt.Sprintf("Getting files from cloud storage by prefix '%s'", prefix), s3.logMetaData)

	return s3.getListObjects(true, prefix, condition)
}

// Don't forget to close the object after using it
func (s3 *s3Client) GetDownloadObject(fileName string) (CloudStorageObject, int64, error) {
	s3.logger.LogInfo(fmt.Sprintf("Getting file '%s' from cloud storage", fileName), s3.logMetaData)

	// Get the reader for the file itself
	object, err := s3.client.GetObject(s3.ctx, s3.bucketName, fileName, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get object '%s': %s", fileName, err)
	}

	// Get the metadata of the object to get the file size
	info, err := object.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get object '%s' metadata: %s", fileName, err)
	}

	s3.logger.LogInfo(fmt.Sprintf("Got file '%s' from cloud storage successfully", fileName), s3.logMetaData)

	return object, info.Size, nil
}

func (s3 *s3Client) UploadFile(fileName, filePath string) (string, error) {
	s3.logger.LogInfo(fmt.Sprintf("Uploading file '%s' to cloud storage", fileName), s3.logMetaData)

	info, err := s3.client.FPutObject(s3.ctx, s3.bucketName, fileName, filePath, minio.PutObjectOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to upload file '%s': %s", fileName, err)
	}

	s3.logger.LogInfo(fmt.Sprintf("File '%s' uploaded to cloud storage successfully", fileName), s3.logMetaData)

	return info.ETag, nil
}

func (s3 *s3Client) DownloadFile(fileName, outputPath string) error {
	s3.logger.LogInfo(fmt.Sprintf("Downloading file '%s' from cloud storage to path '%s'", fileName, outputPath), s3.logMetaData)

	if err := s3.client.FGetObject(s3.ctx, s3.bucketName, fileName, outputPath, minio.GetObjectOptions{}); err != nil {
		return fmt.Errorf("failed to download file '%s': %s", fileName, err)
	}

	s3.logger.LogInfo(fmt.Sprintf("File '%s' downloaded from cloud storage successfully", fileName), s3.logMetaData)
	return nil
}

func (s3 *s3Client) DeleteFile(fileName string) error {
	s3.logger.LogInfo(fmt.Sprintf("Deleting file '%s' from cloud storage", fileName), s3.logMetaData)

	if err := s3.client.RemoveObject(s3.ctx, s3.bucketName, fileName, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("failed to delete file '%s': %s", fileName, err)
	}

	s3.logger.LogInfo(fmt.Sprintf("File '%s' deleted from cloud storage successfully", fileName), s3.logMetaData)
	return nil
}

// PutObject uploads directly from r, honoring ctx cancellation — unlike
// UploadFile, no local file path is required. Content-Type is auto-detected
// from key's extension, matching UploadFile/FPutObject's own behavior;
// path.Ext (not filepath.Ext) because key is an S3 key, always "/"-separated
// regardless of the host OS, not a local filesystem path.
func (s3 *s3Client) PutObject(ctx context.Context, key string, r io.Reader, size int64) (string, error) {
	s3.logger.LogInfo(fmt.Sprintf("Uploading object '%s' to cloud storage", key), s3.logMetaData)

	contentType := mime.TypeByExtension(path.Ext(key))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	info, err := s3.client.PutObject(ctx, s3.bucketName, key, r, size, minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", fmt.Errorf("failed to upload object '%s': %w", key, err)
	}

	s3.logger.LogInfo(fmt.Sprintf("Object '%s' uploaded to cloud storage successfully", key), s3.logMetaData)
	return info.ETag, nil
}

// GetObject is GetDownloadObject with ctx cancellation.
//
// Don't forget to close the object after using it.
func (s3 *s3Client) GetObject(ctx context.Context, key string) (CloudStorageObject, int64, error) {
	s3.logger.LogInfo(fmt.Sprintf("Getting object '%s' from cloud storage", key), s3.logMetaData)

	// GetObject itself does no I/O and so cannot fail on a missing key —
	// minio-go resolves that lazily, on the first Read or (as here) Stat.
	object, err := s3.client.GetObject(ctx, s3.bucketName, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get object '%s': %w", key, wrapNotFound(err))
	}

	info, err := object.Stat()
	if err != nil {
		// minio-go's internal feeder goroutine for this object only exits
		// via Close() — its request loop has no ctx.Done() case. If Stat
		// failed because ctx was already done (a routine race: the caller's
		// HTTP client disconnected between GetObject and Stat), the
		// goroutine never received a request and is still blocked waiting
		// for one. Dropping object here without closing it leaks that
		// goroutine, and its channels, permanently.
		_ = object.Close()
		return nil, 0, fmt.Errorf("failed to get object '%s' metadata: %w", key, wrapNotFound(err))
	}

	s3.logger.LogInfo(fmt.Sprintf("Got object '%s' from cloud storage successfully", key), s3.logMetaData)
	return object, info.Size, nil
}

// StatObject reports key's size without downloading it.
func (s3 *s3Client) StatObject(ctx context.Context, key string) (int64, error) {
	s3.logger.LogInfo(fmt.Sprintf("Statting object '%s' in cloud storage", key), s3.logMetaData)

	info, err := s3.client.StatObject(ctx, s3.bucketName, key, minio.StatObjectOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to stat object '%s': %w", key, wrapNotFound(err))
	}

	s3.logger.LogInfo(fmt.Sprintf("Statted object '%s' in cloud storage successfully, size=%d", key, info.Size), s3.logMetaData)
	return info.Size, nil
}

// DeleteObject is DeleteFile with ctx cancellation. Unlike GetObject and
// StatObject, a missing key is never classified as ErrNotFound here: S3
// answers success for a DELETE on an object that was never there (minio-go's
// own RemoveObject documents this), so the only error this can return is a
// genuine failure — most commonly a missing bucket, which is not what a
// caller checking errors.Is(err, ErrNotFound) means by "not found".
func (s3 *s3Client) DeleteObject(ctx context.Context, key string) error {
	s3.logger.LogInfo(fmt.Sprintf("Deleting object '%s' from cloud storage", key), s3.logMetaData)

	if err := s3.client.RemoveObject(ctx, s3.bucketName, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("failed to delete object '%s': %w", key, err)
	}

	s3.logger.LogInfo(fmt.Sprintf("Object '%s' deleted from cloud storage successfully", key), s3.logMetaData)
	return nil
}

// PresignGetURL returns a time-limited URL a third party can GET directly.
// Never wraps ErrNotFound: presigning never consults the object itself
// (see the interface doc comment) — the only error reachable here is a
// bucket-location lookup failure, most commonly a missing bucket, which is
// not what a caller checking errors.Is(err, ErrNotFound) means by
// "not found". Never log u itself: the URL embeds a signature that is
// usable as a bearer credential for its lifetime.
func (s3 *s3Client) PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	s3.logger.LogInfo(fmt.Sprintf("Presigning GET for object '%s', expiry=%s", key, expiry), s3.logMetaData)

	u, err := s3.client.PresignedGetObject(ctx, s3.bucketName, key, expiry, nil)
	if err != nil {
		return "", fmt.Errorf("failed to presign GET for object '%s': %w", key, err)
	}

	s3.logger.LogInfo(fmt.Sprintf("Presigned GET for object '%s' successfully", key), s3.logMetaData)
	return u.String(), nil
}

// PresignPutURL returns a time-limited URL a third party can PUT directly.
// Same ErrNotFound and logging notes as PresignGetURL.
func (s3 *s3Client) PresignPutURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	s3.logger.LogInfo(fmt.Sprintf("Presigning PUT for object '%s', expiry=%s", key, expiry), s3.logMetaData)

	u, err := s3.client.PresignedPutObject(ctx, s3.bucketName, key, expiry)
	if err != nil {
		return "", fmt.Errorf("failed to presign PUT for object '%s': %w", key, err)
	}

	s3.logger.LogInfo(fmt.Sprintf("Presigned PUT for object '%s' successfully", key), s3.logMetaData)
	return u.String(), nil
}

// PRIVATE //

// wrapNotFound classifies err by HTTP status rather than a provider-specific
// error code (MinIO's "NoSuchKey" vs. another S3-compatible backend's own
// vocabulary), so ErrNotFound holds across every backend this package
// targets. A non-404 error is returned unchanged. Only meaningful where a
// 404 genuinely means "this key doesn't exist" — GetObject and StatObject;
// see DeleteObject's and the presign methods' own doc comments for why they
// deliberately don't use this.
//
// %w wraps err itself, not just this function's own message: ToErrorResponse
// is a plain type switch, not errors.As, so it only recognizes err when it's
// the concrete minio.ErrorResponse value, not one wrapped further beneath
// %w — chaining it here (rather than %s'ing it to text) is what keeps a
// caller's own errors.As(err, &minio.ErrorResponse{}) working afterward.
func wrapNotFound(err error) error {
	if minio.ToErrorResponse(err).StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}

func (s3 *s3Client) getListObjects(isRecursive bool, prefix string, condition func(name string) bool) ([]string, error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	options := minio.ListObjectsOptions{Recursive: isRecursive}
	if prefix != "" {
		options.Prefix = prefix
	}
	objectCh := s3.client.ListObjects(ctx, s3.bucketName, options)

	var objectNames []string
	for object := range objectCh {
		if object.Err != nil {
			return nil, object.Err
		}

		if condition == nil || condition(object.Key) {
			objectNames = append(objectNames, object.Key)
		}
	}

	return objectNames, nil
}
