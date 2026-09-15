package cloud_storage

import (
	"errors"
	"io"
	"time"

	"github.com/LabOS-co/go-packages/logs"
)

type CloudStorageClient interface {
	GetFileNames() ([]string, error)
	GetFolderNames() ([]string, error)
	GetFileNamesByPrefixAndCondition(prefix string, condition func(name string) bool) ([]string, error)
	GetDownloadObject(fileName string) (CloudStorageObject, int64, error)
	UploadFile(fileName, filePath string) (string, error)
	DownloadFile(fileName, outputPath string) error
	DeleteFile(fileName string) error

	// PresignGetURL returns a time-limited URL a third party can GET
	// directly, without ever holding our credentials. expiry must be
	// between 1 second and 7 days; outside that range the underlying
	// signer rejects it.
	PresignGetURL(key string, expiry time.Duration) (string, error)

	// PresignPutURL returns a time-limited URL a third party can PUT
	// directly, without ever holding our credentials. Same expiry bound as
	// PresignGetURL.
	PresignPutURL(key string, expiry time.Duration) (string, error)
}

// ErrNotFound marks a definite "the key doesn't exist" response from the
// store, wrapped into GetDownloadObject's error. Check with errors.Is.
var ErrNotFound = errors.New("cloud_storage: key not found")

type CloudStorageCredentials struct {
	Id     string // account_id
	Secret string // app_id
}

type CloudStorageSettings struct {
	Url         string
	BucketName  string
	Credentials *CloudStorageCredentials
	Logger      logs.Logger
	LogMetaData *logs.LogMetaData

	// Insecure disables TLS (plain http://) for the underlying MinIO client.
	// Default false preserves this package's original hardcoded-secure
	// behavior; set true only for a local/dev MinIO with no TLS in front.
	Insecure bool

	// Region is passed through to the underlying MinIO client as-is, and
	// used to sign presigned requests. Empty (the default) lets minio-go
	// auto-detect it via a live GetBucketLocation lookup on first use.
	Region string
}

type CloudStorageObject interface {
	io.Reader
	Close() error
}
