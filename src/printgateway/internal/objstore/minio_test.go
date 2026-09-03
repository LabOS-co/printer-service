package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LabOS-co/go-packages/cloud_storage"
	"github.com/LabOS-co/go-packages/logs"

	"printgateway/internal/apperr"
)

// fakeObject is the cloud_storage.CloudStorageObject fakeClient.GetObject
// hands back. MinIO.Get does nothing with it beyond passing it through, so
// there is nothing to assert about its behavior here — only that the exact
// value the client returned comes back out of Get unchanged.
type fakeObject struct {
	io.Reader
}

func (fakeObject) Close() error { return nil }

// fakeClient implements cloud_storage.CloudStorageStreamingClient.
// objstore.MinIO only ever calls GetObject/PresignGetURL/PresignPutURL, so
// every other method panics if invoked — a call there would mean MinIO
// started depending on a capability neither this package nor printgw's own
// ObjectStore/httpapi.Presigner ports currently need.
type fakeClient struct {
	getObjectResult cloud_storage.CloudStorageObject
	getObjectSize   int64
	getObjectErr    error
	getObjectCtx    context.Context
	getObjectKey    string

	presignGetURL string
	presignGetErr error
	presignGetCtx context.Context
	presignGetKey string
	presignGetTTL time.Duration

	presignPutURL string
	presignPutErr error
	presignPutCtx context.Context
	presignPutKey string
	presignPutTTL time.Duration
}

func (f *fakeClient) GetObject(ctx context.Context, key string) (cloud_storage.CloudStorageObject, int64, error) {
	f.getObjectCtx = ctx
	f.getObjectKey = key
	if f.getObjectErr != nil {
		return nil, 0, f.getObjectErr
	}
	return f.getObjectResult, f.getObjectSize, nil
}

func (f *fakeClient) PresignGetURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	f.presignGetCtx = ctx
	f.presignGetKey = key
	f.presignGetTTL = expiry
	if f.presignGetErr != nil {
		return "", f.presignGetErr
	}
	return f.presignGetURL, nil
}

func (f *fakeClient) PresignPutURL(ctx context.Context, key string, expiry time.Duration) (string, error) {
	f.presignPutCtx = ctx
	f.presignPutKey = key
	f.presignPutTTL = expiry
	if f.presignPutErr != nil {
		return "", f.presignPutErr
	}
	return f.presignPutURL, nil
}

func (f *fakeClient) unimplemented(name string) {
	panic(fmt.Sprintf("fakeClient.%s: not used by objstore.MinIO, should never be called", name))
}

func (f *fakeClient) GetFileNames() ([]string, error) {
	f.unimplemented("GetFileNames")
	return nil, nil
}

func (f *fakeClient) GetFolderNames() ([]string, error) {
	f.unimplemented("GetFolderNames")
	return nil, nil
}

func (f *fakeClient) GetFileNamesByPrefixAndCondition(prefix string, condition func(name string) bool) ([]string, error) {
	f.unimplemented("GetFileNamesByPrefixAndCondition")
	return nil, nil
}

func (f *fakeClient) GetDownloadObject(fileName string) (cloud_storage.CloudStorageObject, int64, error) {
	f.unimplemented("GetDownloadObject")
	return nil, 0, nil
}

func (f *fakeClient) UploadFile(fileName, filePath string) (string, error) {
	f.unimplemented("UploadFile")
	return "", nil
}

func (f *fakeClient) DownloadFile(fileName, outputPath string) error {
	f.unimplemented("DownloadFile")
	return nil
}

func (f *fakeClient) DeleteFile(fileName string) error {
	f.unimplemented("DeleteFile")
	return nil
}

func (f *fakeClient) PutObject(ctx context.Context, key string, r io.Reader, size int64) (string, error) {
	f.unimplemented("PutObject")
	return "", nil
}

func (f *fakeClient) StatObject(ctx context.Context, key string) (int64, error) {
	f.unimplemented("StatObject")
	return 0, nil
}

func (f *fakeClient) DeleteObject(ctx context.Context, key string) error {
	f.unimplemented("DeleteObject")
	return nil
}

var _ cloud_storage.CloudStorageStreamingClient = (*fakeClient)(nil)

// ctxKey distinguishes a test's ctx from context.Background() itself.
// context.Background() returns the same singleton on every call, so
// asserting `gotCtx != context.Background()` would still pass against a
// mutant that discarded the caller's ctx and substituted its own
// context.Background() — the comparison needs a ctx no code under test could
// plausibly reconstruct.
type ctxKey struct{}

func probeCtx() context.Context {
	return context.WithValue(context.Background(), ctxKey{}, "probe")
}

func TestGetSuccess(t *testing.T) {
	t.Parallel()

	obj := fakeObject{Reader: strings.NewReader("hello")}
	client := &fakeClient{getObjectResult: obj, getObjectSize: 42}
	m := &MinIO{client: client}

	ctx := probeCtx()
	got, size, err := m.Get(ctx, "some/key")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got != obj {
		t.Error("returned object is not the one the client produced")
	}
	if size != 42 {
		t.Errorf("size = %d, want 42", size)
	}
	if client.getObjectKey != "some/key" {
		t.Errorf("client saw key %q, want %q", client.getObjectKey, "some/key")
	}
	if client.getObjectCtx != ctx {
		t.Error("ctx was not passed through unchanged")
	}
}

func TestGetNotFound(t *testing.T) {
	t.Parallel()

	underlying := fmt.Errorf("minio: key %q: %w", "missing/key", cloud_storage.ErrNotFound)
	client := &fakeClient{getObjectErr: underlying}
	m := &MinIO{client: client}

	got, _, err := m.Get(context.Background(), "missing/key")
	if got != nil {
		t.Error("Get returned a non-nil object alongside an error")
	}

	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *apperr.HTTPError", err)
	}
	if httpErr.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", httpErr.Status, http.StatusNotFound)
	}
	if !strings.Contains(httpErr.Public, "missing/key") {
		t.Errorf("Public = %q, want it to name the key", httpErr.Public)
	}
	if !errors.Is(err, cloud_storage.ErrNotFound) {
		t.Error("err does not unwrap to cloud_storage.ErrNotFound — a caller checking errors.Is would miss it")
	}
}

func TestGetOtherErrorIsBadGateway(t *testing.T) {
	t.Parallel()

	underlying := errors.New("connection reset by peer")
	client := &fakeClient{getObjectErr: underlying}
	m := &MinIO{client: client}

	got, _, err := m.Get(context.Background(), "some/key")
	if got != nil {
		t.Error("Get returned a non-nil object alongside an error")
	}

	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *apperr.HTTPError", err)
	}
	if httpErr.Status != http.StatusBadGateway {
		t.Errorf("Status = %d, want %d", httpErr.Status, http.StatusBadGateway)
	}
	if httpErr.Public != "failed to fetch object from storage" {
		t.Errorf("Public = %q, want a generic message — the underlying transport error must not reach the client", httpErr.Public)
	}
	if !errors.Is(err, underlying) {
		t.Error("Internal does not preserve the underlying error for the log")
	}
}

func TestPresignGetSuccess(t *testing.T) {
	t.Parallel()

	client := &fakeClient{presignGetURL: "https://example.com/signed-get"}
	m := &MinIO{client: client}

	ctx := probeCtx()
	url, err := m.PresignGet(ctx, "some/key", 5*time.Minute)
	if err != nil {
		t.Fatalf("PresignGet returned error: %v", err)
	}
	if url != "https://example.com/signed-get" {
		t.Errorf("url = %q", url)
	}
	if client.presignGetKey != "some/key" {
		t.Errorf("client saw key %q, want %q", client.presignGetKey, "some/key")
	}
	if client.presignGetTTL != 5*time.Minute {
		t.Errorf("client saw ttl %v, want %v", client.presignGetTTL, 5*time.Minute)
	}
	if client.presignGetCtx != ctx {
		t.Error("ctx was not passed through unchanged")
	}
}

func TestPresignGetError(t *testing.T) {
	t.Parallel()

	underlying := errors.New("bucket lookup failed")
	client := &fakeClient{presignGetErr: underlying}
	m := &MinIO{client: client}

	_, err := m.PresignGet(context.Background(), "some/key", time.Minute)

	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *apperr.HTTPError", err)
	}
	if httpErr.Status != http.StatusBadGateway {
		t.Errorf("Status = %d, want %d", httpErr.Status, http.StatusBadGateway)
	}
	if httpErr.Public != "failed to presign object URL" {
		t.Errorf("Public = %q, want a generic message", httpErr.Public)
	}
	if !errors.Is(err, underlying) {
		t.Error("Internal does not preserve the underlying error for the log")
	}
}

func TestPresignPutSuccess(t *testing.T) {
	t.Parallel()

	client := &fakeClient{presignPutURL: "https://example.com/signed-put"}
	m := &MinIO{client: client}

	ctx := probeCtx()
	url, err := m.PresignPut(ctx, "some/key", time.Hour)
	if err != nil {
		t.Fatalf("PresignPut returned error: %v", err)
	}
	if url != "https://example.com/signed-put" {
		t.Errorf("url = %q", url)
	}
	if client.presignPutKey != "some/key" {
		t.Errorf("client saw key %q, want %q", client.presignPutKey, "some/key")
	}
	if client.presignPutTTL != time.Hour {
		t.Errorf("client saw ttl %v, want %v", client.presignPutTTL, time.Hour)
	}
	if client.presignPutCtx != ctx {
		t.Error("ctx was not passed through unchanged")
	}
}

func TestPresignPutError(t *testing.T) {
	t.Parallel()

	underlying := errors.New("signer rejected the request")
	client := &fakeClient{presignPutErr: underlying}
	m := &MinIO{client: client}

	_, err := m.PresignPut(context.Background(), "some/key", time.Minute)

	var httpErr *apperr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v, want *apperr.HTTPError", err)
	}
	if httpErr.Status != http.StatusBadGateway {
		t.Errorf("Status = %d, want %d", httpErr.Status, http.StatusBadGateway)
	}
	if httpErr.Public != "failed to presign object URL" {
		t.Errorf("Public = %q, want a generic message", httpErr.Public)
	}
	if !errors.Is(err, underlying) {
		t.Error("Internal does not preserve the underlying error for the log")
	}
}

// TestNewRejectsIncompleteSettings covers New's own responsibility — passing
// settings through to cloud_storage.NewS3 — without needing a reachable S3
// endpoint: NewS3 validates its arguments before constructing anything, and
// the only network-capable call it makes is minio.New, which builds a client
// value locally and does not itself dial out (verified by reading
// s3_client.go — NewS3 carries no doc comment of its own on this point).
func TestNewRejectsIncompleteSettings(t *testing.T) {
	t.Parallel()

	_, err := New("", "bucket", "id", "secret", "", false, logs.LoggerMock{}, nil)
	if err == nil {
		t.Fatal("New with an empty url returned no error")
	}
}

func TestNewBuildsAStoreOnValidSettings(t *testing.T) {
	t.Parallel()

	store, err := New("localhost:9000", "bucket", "id", "secret", "", true, logs.LoggerMock{}, nil)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if store == nil {
		t.Fatal("New returned a nil store with no error")
	}
}

// capturingLogger records NewS3's own startup lines — the only externally
// observable evidence of how New mapped its positional arguments onto
// cloud_storage.CloudStorageSettings, since New has no seam of its own (it
// calls the concrete cloud_storage.NewS3 directly, unlike MinIO.client,
// which is an injectable interface field). NewS3 always logs
// "Initializing S3 client with url '%s' and bucket '%s'" and, only when
// Insecure is set, a separate loud warning — together those catch a
// url/bucket transposition and a wrong/inverted Insecure value, both
// realistic mistakes given New's 8 positional, mostly-string parameters.
// Credentials and Region are never logged by NewS3, so a transposed
// accessKey/secretKey or a dropped Region stays unverifiable from here.
type capturingLogger struct {
	logs.LoggerMock
	infos []string
}

func (c *capturingLogger) LogInfo(msg string, _ *logs.LogMetaData) error {
	c.infos = append(c.infos, msg)
	return nil
}

func TestNewPassesURLBucketAndInsecureThrough(t *testing.T) {
	t.Parallel()

	for _, insecure := range []bool{false, true} {
		insecure := insecure
		t.Run(fmt.Sprintf("insecure=%v", insecure), func(t *testing.T) {
			t.Parallel()

			log := &capturingLogger{}
			if _, err := New("localhost:9000", "my-bucket", "id", "secret", "", insecure, log, nil); err != nil {
				t.Fatalf("New returned error: %v", err)
			}

			all := strings.Join(log.infos, "\n")
			// Positional, not just "both substrings present somewhere":
			// catches a url<->bucket transposition, not only a dropped one.
			if !strings.Contains(all, `url 'localhost:9000' and bucket 'my-bucket'`) {
				t.Errorf("url/bucket not mapped as expected; NewS3 logged:\n%s", all)
			}
			if got := strings.Contains(all, "Insecure=true"); got != insecure {
				t.Errorf("insecure=%v but the Insecure=true warning present=%v — New must not "+
					"invert or hardcode Insecure; a wrong value here silently drops TLS", insecure, got)
			}
		})
	}
}
