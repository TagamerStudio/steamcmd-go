package steamcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var maxDownloadBytes int64 = 1 << 30

type downloadTempFile interface {
	io.Writer
	Name() string
	Sync() error
	Close() error
}

var (
	createDownloadTemp = func(dir, pattern string) (downloadTempFile, error) {
		return os.CreateTemp(dir, pattern)
	}
	downloadLstat   = os.Lstat
	downloadRename  = os.Rename
	downloadRemove  = os.Remove
	downloadReplace = replaceFile
)

func defaultDownloadURLs(platform Platform) []string {
	name := "steamcmd.zip"
	if platform == PlatformLinux {
		name = "steamcmd_linux.tar.gz"
	}
	return []string{
		"https://steamcdn-a.akamaihd.net/client/installer/" + name,
		"https://media.steampowered.com/client/installer/" + name,
		"https://cdn.cloudflare.steamstatic.com/client/installer/" + name,
	}
}

func (c *Client) downloadWithRetries(ctx context.Context, destination string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	downloadCtx := ctx
	cancel := func() {}
	if c.downloadTimeout > 0 {
		downloadCtx, cancel = context.WithTimeout(ctx, c.downloadTimeout)
	}
	defer cancel()
	attemptPath, err := createDownloadTemp(filepath.Dir(destination), ".steamcmd-attempt-*")
	if err != nil {
		return err
	}
	attemptName := attemptPath.Name()
	if err := attemptPath.Close(); err != nil {
		_ = os.RemoveAll(attemptName)
		return err
	}
	_ = os.RemoveAll(attemptName)
	defer func() { _ = os.RemoveAll(attemptName) }()
	var lastErr error
	for _, url := range c.downloadURLs {
		for attempt := 0; attempt < c.attempts; attempt++ {
			err := c.downloadAttempt(downloadCtx, url, attemptName, destination, attempt)
			if err == nil {
				return nil
			}
			if downloadCtx.Err() != nil {
				return downloadCtx.Err()
			}
			lastErr = err
		}
	}
	if lastErr == nil {
		return errors.New("no download URLs configured")
	}
	return lastErr
}

func (c *Client) downloadAttempt(ctx context.Context, url, attemptName, destination string, attempt int) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := waitForDownloadRetry(ctx, c.retryBackoff, attempt); err != nil {
		return err
	}
	_ = os.RemoveAll(attemptName)
	if err := c.downloadOnce(ctx, url, attemptName); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	info, statErr := downloadLstat(attemptName)
	if statErr != nil {
		return errors.New("downloader completed without creating a file")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("downloader created a non-regular file")
	}
	if info.Size() > maxDownloadBytes {
		return fmt.Errorf("download exceeds maximum size of %d bytes", maxDownloadBytes)
	}
	return downloadReplace(attemptName, destination)
}

func waitForDownloadRetry(ctx context.Context, backoff time.Duration, attempt int) error {
	if attempt == 0 || backoff <= 0 {
		return nil
	}
	wait := backoff * time.Duration(1<<(attempt-1))
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) downloadOnce(ctx context.Context, url, destination string) error {
	if c.downloader != nil {
		return c.downloader(ctx, url, destination)
	}
	return httpDownload(ctx, c.httpClient, c.downloadTimeout, url, destination)
}

func httpDownload(ctx context.Context, configured *http.Client, timeout time.Duration, url, destination string) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	requestCtx := ctx
	cancel := func() {}
	if timeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	client := configured
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "TagamerStudio/steamcmd-go")
	response, err := client.Do(request)
	if err != nil {
		if requestCtx.Err() != nil {
			return requestCtx.Err()
		}
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download returned HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > maxDownloadBytes {
		return fmt.Errorf("download exceeds maximum size of %d bytes", maxDownloadBytes)
	}
	return writeHTTPDownload(requestCtx, response, destination)
}

func writeHTTPDownload(ctx context.Context, response *http.Response, destination string) error {
	dir := filepath.Dir(destination)
	temporary, err := createDownloadTemp(dir, ".steamcmd-download-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()

	written, err := io.Copy(temporary, io.LimitReader(response.Body, maxDownloadBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if written > maxDownloadBytes {
		return fmt.Errorf("download exceeds maximum size of %d bytes", maxDownloadBytes)
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := downloadReplace(temporaryPath, destination); err != nil {
		return err
	}
	keep = true
	return nil
}

func replaceFile(source, destination string) error {
	if err := downloadRename(source, destination); err == nil {
		return nil
	}
	old, err := downloadLstat(destination)
	if err != nil {
		return err
	}
	if !old.Mode().IsRegular() || old.Mode()&os.ModeSymlink != 0 {
		return errors.New("download destination is not a regular file")
	}
	backup, err := createDownloadTemp(filepath.Dir(destination), ".steamcmd-download-old-*")
	if err != nil {
		return err
	}
	backupName := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupName)
		return err
	}
	if err := downloadRemove(backupName); err != nil {
		return err
	}
	if err := downloadRename(destination, backupName); err != nil {
		return err
	}
	if err := downloadRename(source, destination); err != nil {
		_ = downloadRename(backupName, destination)
		return err
	}
	_ = downloadRemove(backupName)
	return nil
}
