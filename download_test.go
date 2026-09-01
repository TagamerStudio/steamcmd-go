package steamcmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHTTPDownloadFailurePreservesDestination(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "archive")
	if err := os.WriteFile(destination, []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("failure"))
	}))
	defer server.Close()
	if err := httpDownload(context.Background(), server.Client(), 0, server.URL, destination); err == nil {
		t.Fatal("HTTP failure was accepted")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old content" {
		t.Fatalf("destination = %q, want old content", data)
	}
}

func TestHTTPDownloadSizeLimitPreservesDestination(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "archive")
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "1073741825")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	if err := httpDownload(context.Background(), server.Client(), 0, server.URL, destination); err == nil {
		t.Fatal("oversized HTTP response was accepted")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "old" {
		t.Fatalf("destination = %q, want old", data)
	}
}

func TestDownloaderRetriesAndAtomicPublish(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "archive")
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	attempts := 0
	client, err := New(Config{
		SteamCMDDir:  filepath.Join(root, "steamcmd"),
		InstallDir:   filepath.Join(root, "install"),
		DownloadURLs: []string{"one"},
		Attempts:     2,
		Downloader: func(_ context.Context, _ string, path string) error {
			attempts++
			if attempts == 1 {
				_ = os.WriteFile(path, []byte("partial"), 0o644)
				return errors.New("temporary failure")
			}
			return os.WriteFile(path, []byte("new"), 0o644)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.downloadWithRetries(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" || attempts != 2 {
		t.Fatalf("destination = %q, attempts = %d", data, attempts)
	}
}

func TestDownloaderMirrorsAndCancellation(t *testing.T) {
	root := t.TempDir()
	called := []string{}
	client, err := New(Config{
		SteamCMDDir:  filepath.Join(root, "steamcmd"),
		InstallDir:   filepath.Join(root, "install"),
		DownloadURLs: []string{"first", "second"},
		Attempts:     1,
		Downloader: func(ctx context.Context, url, _ string) error {
			called = append(called, url)
			if url == "first" {
				return os.ErrNotExist
			}
			return ctx.Err()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.downloadWithRetries(ctx, filepath.Join(root, "target"))
	if err == nil || len(called) != 0 {
		t.Fatalf("canceled download error = %v, calls = %v", err, called)
	}
}

func TestDownloadHTTPPaths(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "download")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.Header.Get("User-Agent") != "TagamerStudio/steamcmd-go" {
			t.Errorf("request = %s %q", request.Method, request.Header.Get("User-Agent"))
		}
		_, _ = io.WriteString(writer, "downloaded")
	}))
	defer server.Close()
	if err := httpDownload(context.Background(), nil, 0, server.URL, destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "downloaded" {
		t.Fatalf("download = %q, %v", data, err)
	}
	canceledContext, cancelCanceledContext := context.WithCancel(context.Background())
	cancelCanceledContext()
	if err := httpDownload(canceledContext, nil, 0, server.URL, filepath.Join(root, "already-canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled download error = %v", err)
	}
	if err := (&Client{httpClient: server.Client()}).downloadOnce(context.Background(), server.URL, filepath.Join(root, "download-once")); err != nil {
		t.Fatal(err)
	}

	if err := httpDownload(context.Background(), server.Client(), time.Second, server.URL, filepath.Join(root, "timed")); err != nil {
		t.Fatal(err)
	}
	if err := httpDownload(context.Background(), server.Client(), 0, "://bad", filepath.Join(root, "invalid")); err == nil {
		t.Fatal("invalid URL was accepted")
	}

	transportError := errors.New("transport failure")
	transportClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, transportError
	})}
	if err := httpDownload(context.Background(), transportClient, 0, "http://example.test", filepath.Join(root, "transport")); !errors.Is(err, transportError) {
		t.Fatalf("transport error = %v", err)
	}

	requestCanceled, cancel := context.WithCancel(context.Background())
	cancelingClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return nil, transportError
	})}
	if err := httpDownload(requestCanceled, cancelingClient, 0, "http://example.test", filepath.Join(root, "canceled")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled transport error = %v", err)
	}

	oldCreate := createDownloadTemp
	oldReplace := downloadReplace
	oldMax := maxDownloadBytes
	t.Cleanup(func() {
		createDownloadTemp = oldCreate
		downloadReplace = oldReplace
		maxDownloadBytes = oldMax
	})
	maxDownloadBytes = 3
	createDownloadTemp = func(_, _ string) (downloadTempFile, error) {
		return &fakeDownloadTemp{name: filepath.Join(root, "fake")}, nil
	}
	unknownLength := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: -1,
			Body:          io.NopCloser(strings.NewReader("1234")),
		}, nil
	})}
	smallLength := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: -1,
			Body:          io.NopCloser(strings.NewReader("12")),
		}, nil
	})}
	if err := httpDownload(context.Background(), unknownLength, 0, "http://example.test", filepath.Join(root, "too-large")); err == nil {
		t.Fatal("unknown-length oversized response was accepted")
	}

	createDownloadTemp = func(_, _ string) (downloadTempFile, error) {
		return nil, errors.New("temporary file failure")
	}
	if err := httpDownload(context.Background(), unknownLength, 0, "http://example.test", filepath.Join(root, "temp-failure")); err == nil {
		t.Fatal("temporary file failure was ignored")
	}

	createDownloadTemp = func(_, _ string) (downloadTempFile, error) {
		return &fakeDownloadTemp{name: filepath.Join(root, "copy-error")}, nil
	}
	copyClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: &errorReadCloser{}}, nil
	})}
	if err := httpDownload(context.Background(), copyClient, 0, "http://example.test", filepath.Join(root, "copy-error-destination")); err == nil {
		t.Fatal("body read failure was ignored")
	}

	copyCanceled, cancelCopy := context.WithCancel(context.Background())
	contextCopyClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: &cancelReadCloser{cancel: cancelCopy}}, nil
	})}
	if err := httpDownload(copyCanceled, contextCopyClient, 0, "http://example.test", filepath.Join(root, "context-copy-error")); !errors.Is(err, context.Canceled) {
		t.Fatalf("context body error = %v", err)
	}

	createDownloadTemp = func(_, _ string) (downloadTempFile, error) {
		return &fakeDownloadTemp{name: filepath.Join(root, "sync-error"), syncErr: errors.New("sync failure")}, nil
	}
	if err := httpDownload(context.Background(), smallLength, 0, "http://example.test", filepath.Join(root, "sync-error-destination")); err == nil {
		t.Fatal("sync failure was ignored")
	}
	createDownloadTemp = func(_, _ string) (downloadTempFile, error) {
		return &fakeDownloadTemp{name: filepath.Join(root, "close-error"), closeErr: errors.New("close failure")}, nil
	}
	if err := httpDownload(context.Background(), smallLength, 0, "http://example.test", filepath.Join(root, "close-error-destination")); err == nil {
		t.Fatal("close failure was ignored")
	}
	createDownloadTemp = func(_, _ string) (downloadTempFile, error) {
		return &fakeDownloadTemp{name: filepath.Join(root, "replace-error")}, nil
	}
	downloadReplace = func(string, string) error { return errors.New("replace failure") }
	if err := httpDownload(context.Background(), smallLength, 0, "http://example.test", filepath.Join(root, "replace-error-destination")); err == nil {
		t.Fatal("replace failure was ignored")
	}
}

func TestDownloadRetriesAndReplacementErrors(t *testing.T) {
	root := t.TempDir()
	oldCreate := createDownloadTemp
	oldLstat := downloadLstat
	oldReplace := downloadReplace
	oldMax := maxDownloadBytes
	t.Cleanup(func() {
		createDownloadTemp = oldCreate
		downloadLstat = oldLstat
		downloadReplace = oldReplace
		maxDownloadBytes = oldMax
	})

	noURLClient := &Client{attempts: 1}
	if err := noURLClient.downloadWithRetries(context.Background(), filepath.Join(root, "no-url")); err == nil {
		t.Fatal("empty URL list was accepted")
	}
	if err := (&Client{attempts: 1}).downloadWithRetries(context.Background(), filepath.Join(root, "missing", "destination")); err == nil {
		t.Fatal("missing temporary directory was accepted")
	}

	createDownloadTemp = func(_, pattern string) (downloadTempFile, error) {
		if strings.Contains(pattern, "attempt") {
			return &fakeDownloadTemp{name: filepath.Join(root, "attempt-seam"), closeErr: errors.New("attempt close failure")}, nil
		}
		return oldCreate(filepath.Join(root, "unused"), pattern)
	}
	if err := (&Client{attempts: 1}).downloadWithRetries(context.Background(), filepath.Join(root, "attempt-close")); err == nil {
		t.Fatal("attempt close failure was ignored")
	}
	createDownloadTemp = oldCreate

	backoffClient := &Client{downloadURLs: []string{"one"}, attempts: 2, retryBackoff: time.Hour, downloader: func(context.Context, string, string) error {
		return errors.New("retry failure")
	}}
	if err := backoffClient.downloadWithRetries(&retryCancelContext{done: make(chan struct{})}, filepath.Join(root, "backoff")); !errors.Is(err, context.Canceled) {
		t.Fatalf("backoff cancellation error = %v", err)
	}
	errorCanceled, cancelError := context.WithCancel(context.Background())
	errorCancelClient := &Client{downloadURLs: []string{"one"}, attempts: 1, downloader: func(context.Context, string, string) error {
		cancelError()
		return errors.New("canceled download failure")
	}}
	if err := errorCancelClient.downloadWithRetries(errorCanceled, filepath.Join(root, "error-cancel")); !errors.Is(err, context.Canceled) {
		t.Fatalf("error cancellation = %v", err)
	}

	timeoutClient := &Client{downloadURLs: []string{"one"}, attempts: 1, downloadTimeout: time.Nanosecond, downloader: func(ctx context.Context, _, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}}
	if err := timeoutClient.downloadWithRetries(context.Background(), filepath.Join(root, "timeout")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("download timeout error = %v", err)
	}

	maxDownloadBytes = 3
	largeClient := &Client{downloadURLs: []string{"one"}, attempts: 1, downloader: func(_ context.Context, _, path string) error {
		return os.WriteFile(path, []byte("1234"), 0o644)
	}}
	if err := largeClient.downloadWithRetries(context.Background(), filepath.Join(root, "large")); err == nil {
		t.Fatal("oversized downloader result was accepted")
	}

	nonRegularClient := &Client{downloadURLs: []string{"one"}, attempts: 1, downloader: func(_ context.Context, _, path string) error {
		return os.Mkdir(path, 0o755)
	}}
	if err := nonRegularClient.downloadWithRetries(context.Background(), filepath.Join(root, "non-regular")); err == nil {
		t.Fatal("non-regular downloader result was accepted")
	}

	canceledAfterDownload, cancelAfterDownload := context.WithCancel(context.Background())
	afterCancelClient := &Client{downloadURLs: []string{"one"}, attempts: 1, downloader: func(_ context.Context, _, path string) error {
		if err := os.WriteFile(path, []byte("ok"), 0o644); err != nil {
			return err
		}
		cancelAfterDownload()
		return nil
	}}
	if err := afterCancelClient.downloadWithRetries(canceledAfterDownload, filepath.Join(root, "after-cancel")); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-download cancellation error = %v", err)
	}

	downloadLstat = func(string) (os.FileInfo, error) { return nil, errors.New("stat failure") }
	statClient := &Client{downloadURLs: []string{"one"}, attempts: 1, downloader: func(_ context.Context, _, _ string) error { return nil }}
	if err := statClient.downloadWithRetries(context.Background(), filepath.Join(root, "stat")); err == nil {
		t.Fatal("stat failure was ignored")
	}
	downloadLstat = oldLstat
	downloadReplace = func(string, string) error { return errors.New("publish failure") }
	publishClient := &Client{downloadURLs: []string{"one"}, attempts: 1, downloader: func(_ context.Context, _, path string) error {
		return os.WriteFile(path, []byte("ok"), 0o644)
	}}
	if err := publishClient.downloadWithRetries(context.Background(), filepath.Join(root, "publish")); err == nil {
		t.Fatal("publish failure was ignored")
	}

	failClient := &Client{downloadURLs: []string{"one", "two"}, attempts: 1, downloader: func(context.Context, string, string) error {
		return errors.New("all failed")
	}}
	if err := failClient.downloadWithRetries(context.Background(), filepath.Join(root, "all-failed")); err == nil {
		t.Fatal("all download failures were ignored")
	}

	oldRename := downloadRename
	oldRemove := downloadRemove
	t.Cleanup(func() {
		downloadRename = oldRename
		downloadRemove = oldRemove
	})
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.WriteFile(source, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	renameCalls := 0
	downloadRename = func(old, new string) error {
		renameCalls++
		if renameCalls == 1 {
			return errors.New("replace unsupported")
		}
		return os.Rename(old, new)
	}
	if err := replaceFile(source, destination); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(destination); err != nil || string(data) != "new" {
		t.Fatalf("replaced destination = %q, %v", data, err)
	}

	if err := replaceFile(filepath.Join(root, "missing-source"), filepath.Join(root, "missing-destination")); err == nil {
		t.Fatal("missing destination was accepted")
	}
	directoryDestination := filepath.Join(root, "directory-destination")
	if err := os.Mkdir(directoryDestination, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceFile(filepath.Join(root, "missing-source"), directoryDestination); err == nil {
		t.Fatal("directory destination was accepted")
	}

	createDownloadTemp = func(_, _ string) (downloadTempFile, error) { return nil, errors.New("backup create failure") }
	backupCreateDestination := filepath.Join(root, "backup-create-destination")
	if err := os.WriteFile(backupCreateDestination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	renameCalls = 0
	if err := replaceFile(filepath.Join(root, "missing-source"), backupCreateDestination); err == nil {
		t.Fatal("backup create failure was ignored")
	}
	createDownloadTemp = func(_, _ string) (downloadTempFile, error) {
		return &fakeDownloadTemp{name: filepath.Join(root, "backup-close"), closeErr: errors.New("backup close failure")}, nil
	}
	backupCloseDestination := filepath.Join(root, "backup-close-destination")
	if err := os.WriteFile(backupCloseDestination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	renameCalls = 0
	if err := replaceFile(filepath.Join(root, "missing-source"), backupCloseDestination); err == nil {
		t.Fatal("backup close failure was ignored")
	}
	createDownloadTemp = oldCreate

	removeDestination := filepath.Join(root, "remove-destination")
	removeSource := filepath.Join(root, "remove-source")
	if err := os.WriteFile(removeDestination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(removeSource, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	renameCalls = 0
	downloadRemove = func(string) error { return errors.New("backup remove failure") }
	if err := replaceFile(removeSource, removeDestination); err == nil {
		t.Fatal("backup remove failure was ignored")
	}
	downloadRemove = oldRemove

	if err := os.WriteFile(removeDestination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(removeSource, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	renameCalls = 0
	downloadRename = func(old, new string) error {
		renameCalls++
		switch renameCalls {
		case 1:
			return errors.New("replace unsupported")
		case 2:
			return errors.New("backup rename failure")
		default:
			return os.Rename(old, new)
		}
	}
	if err := replaceFile(removeSource, removeDestination); err == nil {
		t.Fatal("backup rename failure was ignored")
	}

	if err := os.WriteFile(removeDestination, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(removeSource, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	renameCalls = 0
	downloadRename = func(old, new string) error {
		renameCalls++
		if renameCalls == 1 {
			return errors.New("replace unsupported")
		}
		if renameCalls == 3 {
			return errors.New("new rename failure")
		}
		return os.Rename(old, new)
	}
	if err := replaceFile(removeSource, removeDestination); err == nil {
		t.Fatal("new rename failure was ignored")
	}
}

type fakeDownloadTemp struct {
	data     bytes.Buffer
	name     string
	syncErr  error
	closeErr error
}

func (f *fakeDownloadTemp) Write(data []byte) (int, error) {
	return f.data.Write(data)
}

func (f *fakeDownloadTemp) Name() string {
	return f.name
}

func (f *fakeDownloadTemp) Sync() error {
	return f.syncErr
}

func (f *fakeDownloadTemp) Close() error {
	return f.closeErr
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type errorReadCloser struct{}

func (errorReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("body read failure")
}

func (errorReadCloser) Close() error {
	return nil
}

type cancelReadCloser struct {
	cancel context.CancelFunc
}

func (r *cancelReadCloser) Read([]byte) (int, error) {
	r.cancel()
	return 0, errors.New("body read failure")
}

func (*cancelReadCloser) Close() error {
	return nil
}

type retryCancelContext struct {
	done  chan struct{}
	calls int
}

func (c *retryCancelContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (c *retryCancelContext) Done() <-chan struct{} {
	return c.done
}

func (c *retryCancelContext) Err() error {
	c.calls++
	if c.calls == 4 {
		close(c.done)
	}
	if c.calls >= 5 {
		return context.Canceled
	}
	return nil
}

func (*retryCancelContext) Value(any) any {
	return nil
}
