package cloud_storage

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/LabOS-co/go-packages/logs"
)

// CloudStorageClient is the package's original, path-based surface. These
// seven methods are not ctx-cancellable (they run against an internal
// context.Background()) and never return ErrNotFound — a missing key
// surfaces as a plain, unclassified error, same as before this package
// gained CloudStorageStreamingClient.
type CloudStorageClient interface {
	GetFileNames() ([]string, error)
	GetFolderNames() ([]string, error)
	GetFileNamesByPrefixAndCondition(prefix string, condition func(name string) bool) ([]string, error)
	GetDownloadObject(fileName string) (CloudStorageObject, int64, error)
	UploadFile(fileName, filePath string) (string, error)
	DownloadFile(fileName, outputPath string) error
	DeleteFile(fileName string) error
}

// CloudStorageStreamingClient adds ctx-cancellable streaming and presigned
// URLs on top of CloudStorageClient's original path-based operations.
// NewS3's concrete type satisfies both, so existing code assigning its
// result to a CloudStorageClient-typed variable is unaffected — this
// interface is strictly wider, not a replacement, and nothing already
// implementing CloudStorageClient elsewhere (e.g. a test mock) is required
// to grow these methods.
type CloudStorageStreamingClient interface {
	CloudStorageClient

	// PutObject uploads directly from r (unlike UploadFile, no local file
	// path is required — the caller may be streaming from memory, an
	// upload's request body, or anywhere else io.Reader reaches) and honors
	// ctx cancellation.
	//
	// size >= 0 must match r's actual byte count exactly, or the upload
	// fails. size == -1 streams an unknown length via multipart upload,
	// buffering in 16 MiB parts — fine for occasional use, but that is
	// 16 MiB of resident memory per concurrent unknown-length upload, so
	// pass the real size whenever the caller already knows it (e.g. an
	// inbound request's Content-Length).
	//
	// Content-Type is auto-detected from key's extension, matching
	// UploadFile/FPutObject's existing behavior, falling back to
	// "application/octet-stream" when the extension is unknown or absent.
	PutObject(ctx context.Context, key string, r io.Reader, size int64) (etag string, err error)

	// GetObject is GetDownloadObject with ctx cancellation. Returns an error
	// wrapping ErrNotFound when key does not exist.
	//
	// The returned CloudStorageObject is bound to ctx for its entire read
	// lifetime, not just this call: a Read after ctx is done fails with
	// ctx's error, even if GetObject itself already returned successfully.
	GetObject(ctx context.Context, key string) (CloudStorageObject, int64, error)

	// StatObject reports key's size without downloading it. Returns an
	// error wrapping ErrNotFound when key does not exist.
	StatObject(ctx context.Context, key string) (int64, error)

	// DeleteObject is DeleteFile with ctx cancellation. Deleting a key that
	// does not exist is NOT an error — S3 (and every backend this package
	// targets) answers success for a DELETE on an absent object, so this
	// never returns ErrNotFound. Call StatObject first if the caller needs
	// to know whether the key was actually there.
	DeleteObject(ctx context.Context, key string) error

	// PresignGetURL returns a time-limited URL a third party can GET
	// directly, without ever holding our credentials. expiry must be
	// between 1 second and 7 days; outside that range the underlying
	// signer rejects it.
	//
	// Signing needs no round trip of its own, but minio-go's signer must
	// know the bucket's region first, and that costs more than the request
	// this method's name suggests when CloudStorageSettings.Region is
	// empty:
	//   - The first bucketed call of any kind (not just presigning) pays a
	//     live GetBucketLocation lookup, cached for the client's lifetime —
	//     verified live: presigning against an unreachable host hung on
	//     exactly this lookup until Region was set explicitly.
	//   - Worse than the latency: if that lookup itself fails (e.g. against
	//     Backblaze B2, one of this package's own targets, which doesn't
	//     answer it), minio-go silently assumes "us-east-1" rather than
	//     erroring. A URL signed for the wrong region still comes back as a
	//     successful string here — the failure lands on whoever tries to
	//     use it, with nothing to trace back to this call.
	// Set Region explicitly to avoid both.
	PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error)

	// PresignPutURL returns a time-limited URL a third party can PUT
	// directly, without ever holding our credentials. Same expiry bound and
	// Region caveat as PresignGetURL.
	PresignPutURL(ctx context.Context, key string, expiry time.Duration) (string, error)
}

// ErrNotFound marks a definite "the key doesn't exist" response from the
// store — GetObject and StatObject wrap it in; DeleteObject and the presign
// methods never do (see their doc comments for why). Classified by HTTP
// status (see wrapNotFound in s3_client.go) rather than a provider-specific
// error code, so it holds across every S3-compatible backend this package
// targets (MinIO, Backblaze B2, ...), not just MinIO's own vocabulary.
// Check with errors.Is: the concrete minio-go error is chained underneath
// via %w, still reachable with errors.As for callers that need it (e.g. for
// a request ID in a bug report), but not equal to ErrNotFound itself.
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
	// behavior for every existing caller; set true only for a local/dev
	// MinIO that has no TLS in front of it. NewS3 logs loudly when this is
	// set, since a production deployment left this way is a real exposure.
	Insecure bool

	// Region is passed through to the underlying MinIO client as-is, and
	// used to sign every request, not just presigned ones — see
	// PresignGetURL's doc comment for what an empty value costs. Empty (the
	// default, matching every existing caller) lets minio-go auto-detect it.
	Region string
}

type CloudStorageObject interface {
	io.Reader
	Close() error
}
