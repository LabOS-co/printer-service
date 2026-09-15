package cloud_storage

import (
	"context"
	"fmt"
	"net/url"
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

func NewS3(settings *CloudStorageSettings) (CloudStorageClient, error) {
	if settings == nil {
		return nil, fmt.Errorf("missing settings object. S3 client cannot be initialized")
	}
	if settings.Url == "" || settings.BucketName == "" || settings.Credentials.Id == "" || settings.Credentials.Secret == "" {
		return nil, fmt.Errorf("missing url, bucketName, id or secret. S3 client cannot be initialized")
	}

	settings.Logger.LogInfo(fmt.Sprintf(
		"Initializing S3 client with url '%s' and bucket '%s'", settings.Url, settings.BucketName),
		settings.LogMetaData,
	)
	if settings.Insecure {
		// LogInfo, not a silent default: Insecure disables TLS, and a
		// production deployment left this way by accident is a real
		// exposure worth surfacing on every single startup.
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
		return nil, 0, fmt.Errorf("failed to get object '%s': %w", fileName, wrapNotFound(err))
	}

	// Get the metadata of the object to get the file size
	info, err := object.Stat()
	if err != nil {
		_ = object.Close()
		return nil, 0, fmt.Errorf("failed to get object '%s' metadata: %w", fileName, wrapNotFound(err))
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

// PresignGetURL returns a time-limited URL a third party can GET directly.
func (s3 *s3Client) PresignGetURL(key string, expiry time.Duration) (string, error) {
	u, err := s3.client.PresignedGetObject(s3.ctx, s3.bucketName, key, expiry, url.Values{})
	if err != nil {
		return "", fmt.Errorf("failed to presign GET for object '%s': %s", key, err)
	}
	return u.String(), nil
}

// PresignPutURL returns a time-limited URL a third party can PUT directly.
func (s3 *s3Client) PresignPutURL(key string, expiry time.Duration) (string, error) {
	u, err := s3.client.PresignedPutObject(s3.ctx, s3.bucketName, key, expiry)
	if err != nil {
		return "", fmt.Errorf("failed to presign PUT for object '%s': %s", key, err)
	}
	return u.String(), nil
}

// PRIVATE //

// wrapNotFound wraps err in ErrNotFound when minio-go reports the object
// itself is missing (as opposed to a transport/auth/other failure).
func wrapNotFound(err error) error {
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return fmt.Errorf("%w: %s", ErrNotFound, err)
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
