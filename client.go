package steamcmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Platform identifies the SteamCMD platform override.
type Platform string

const (
	PlatformWindows Platform = "windows"
	PlatformLinux   Platform = "linux"
)

type installTempFile interface {
	Name() string
	Close() error
}

var (
	resolveAbsPath    = filepath.Abs
	resolveRelPath    = filepath.Rel
	createInstallTemp = func(dir, pattern string) (installTempFile, error) {
		return os.CreateTemp(dir, pattern)
	}
	installLstat     = os.Lstat
	installRemove    = os.Remove
	installMkdirAll  = os.MkdirAll
	installMkdirTemp = os.MkdirTemp
	installRename    = os.Rename
	installChmod     = os.Chmod
	readInstallFile  = os.ReadFile
	installDownload  = func(c *Client, ctx context.Context, destination string) error {
		return c.downloadWithRetries(ctx, destination)
	}
	installExtract = extractArchive
)

// Login contains the credentials used for SteamCMD commands. An empty
// Username selects anonymous login.
type Login struct {
	Username  string
	Password  string
	GuardCode string
}

// Downloader downloads url to destination. The destination is a temporary
// path when called by Install.
type Downloader func(context.Context, string, string) error

// CommandRunner is the command execution seam used by tests and callers that
// need to provide their own process runner.
type CommandRunner func(context.Context, string, []string) ([]byte, error)

// Config controls a Client.
type Config struct {
	SteamCMDDir     string
	InstallDir      string
	Platform        Platform
	Login           Login
	DownloadURLs    []string
	HTTPClient      *http.Client
	Downloader      Downloader
	CommandRunner   CommandRunner
	Attempts        int
	RetryBackoff    time.Duration
	CommandTimeout  time.Duration
	DownloadTimeout time.Duration
	MaxOutputBytes  int64
}

// UpdateOptions controls an app update.
type UpdateOptions struct {
	AppID        string
	Beta         string
	BetaPassword string
	Validate     bool
	Force        bool
	ExtraArgs    []string
}

// WorkshopDownloadOptions controls a workshop item download.
type WorkshopDownloadOptions struct {
	AppID           string
	PublishedFileID string
	Validate        bool
}

// UpdateResult describes an update attempt.
type UpdateResult struct {
	AppID            string
	PreviousBuildID  string
	LatestBuildID    string
	InstalledBuildID string
	Updated          bool
	Verified         bool
}

// UpdateStatus describes whether the installed app differs from the public
// build reported by SteamCMD.
type UpdateStatus struct {
	AppID            string
	PreviousBuildID  string
	LatestBuildID    string
	InstalledBuildID string
	UpdateAvailable  bool
}

// CommandError reports a failed SteamCMD invocation. Output is bounded by
// Config.MaxOutputBytes.
type CommandError struct {
	Output    []byte
	Truncated bool
	Err       error

	secrets []string
}

func (e *CommandError) Error() string {
	if e == nil {
		return "steamcmd command failed"
	}
	if e.Err == nil {
		return "steamcmd command failed"
	}
	message := e.Err.Error()
	for _, secret := range e.secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return "steamcmd command failed: " + message
}

// Unwrap exposes the underlying process or context error.
func (e *CommandError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

const (
	defaultAttempts              = 3
	defaultRetryBackoff          = 2 * time.Second
	defaultCommandTimeout        = 10 * time.Minute
	defaultDownloadTimeout       = 5 * time.Minute
	defaultMaxOutputBytes  int64 = 1 << 20
)

// Client controls one SteamCMD installation and its app directory.
type Client struct {
	mu sync.Mutex

	steamCMDDir     string
	installDir      string
	platform        Platform
	login           Login
	downloadURLs    []string
	httpClient      *http.Client
	downloader      Downloader
	commandRunner   CommandRunner
	attempts        int
	retryBackoff    time.Duration
	commandTimeout  time.Duration
	downloadTimeout time.Duration
	maxOutputBytes  int64
}

// New validates config and creates a Client. Platform defaults to runtime.GOOS
// when omitted. SteamCMDDir and InstallDir must not overlap.
func New(config Config) (*Client, error) {
	platform := config.Platform
	if platform == "" {
		platform = Platform(runtime.GOOS)
	}
	if err := validateConfig(config, platform); err != nil {
		return nil, err
	}
	steamCMDDir, err := resolveAbsPath(config.SteamCMDDir)
	if err != nil {
		return nil, fmt.Errorf("resolve SteamCMDDir: %w", err)
	}
	installDir, err := resolveAbsPath(config.InstallDir)
	if err != nil {
		return nil, fmt.Errorf("resolve InstallDir: %w", err)
	}
	if pathsOverlap(steamCMDDir, installDir) {
		return nil, errors.New("SteamCMDDir and InstallDir must not be equal or nested")
	}

	attempts, retryBackoff, commandTimeout, downloadTimeout, maxOutputBytes, urls := clientDefaults(config, platform)

	return &Client{
		steamCMDDir:     steamCMDDir,
		installDir:      installDir,
		platform:        platform,
		login:           config.Login,
		downloadURLs:    urls,
		httpClient:      config.HTTPClient,
		downloader:      config.Downloader,
		commandRunner:   config.CommandRunner,
		attempts:        attempts,
		retryBackoff:    retryBackoff,
		commandTimeout:  commandTimeout,
		downloadTimeout: downloadTimeout,
		maxOutputBytes:  maxOutputBytes,
	}, nil
}

func clientDefaults(config Config, platform Platform) (int, time.Duration, time.Duration, time.Duration, int64, []string) {
	attempts := config.Attempts
	if attempts == 0 {
		attempts = defaultAttempts
	}
	retryBackoff := config.RetryBackoff
	if retryBackoff == 0 {
		retryBackoff = defaultRetryBackoff
	}
	commandTimeout := config.CommandTimeout
	if commandTimeout == 0 {
		commandTimeout = defaultCommandTimeout
	}
	downloadTimeout := config.DownloadTimeout
	if downloadTimeout == 0 {
		downloadTimeout = defaultDownloadTimeout
	}
	maxOutputBytes := config.MaxOutputBytes
	if maxOutputBytes == 0 {
		maxOutputBytes = defaultMaxOutputBytes
	}
	urls := append([]string(nil), config.DownloadURLs...)
	if len(urls) == 0 {
		urls = defaultDownloadURLs(platform)
	}
	return attempts, retryBackoff, commandTimeout, downloadTimeout, maxOutputBytes, urls
}

func validateConfig(config Config, platform Platform) error {
	if platform != PlatformLinux && platform != PlatformWindows {
		return fmt.Errorf("unsupported platform %q", platform)
	}
	if strings.TrimSpace(config.SteamCMDDir) == "" {
		return errors.New("SteamCMDDir is required")
	}
	if strings.TrimSpace(config.InstallDir) == "" {
		return errors.New("InstallDir is required")
	}
	if config.Attempts < 0 {
		return errors.New("attempts cannot be negative")
	}
	if config.RetryBackoff < 0 {
		return errors.New("RetryBackoff cannot be negative")
	}
	if config.CommandTimeout < 0 {
		return errors.New("CommandTimeout cannot be negative")
	}
	if config.DownloadTimeout < 0 {
		return errors.New("DownloadTimeout cannot be negative")
	}
	if config.MaxOutputBytes < 0 {
		return errors.New("MaxOutputBytes cannot be negative")
	}
	if config.Login.Username == "" && (config.Login.Password != "" || config.Login.GuardCode != "") {
		return errors.New("Login credentials require a username")
	}
	if config.Login.Username != "" && config.Login.Password == "" {
		return errors.New("Login.Password is required for authenticated login")
	}
	return nil
}

func pathsOverlap(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	return pathContains(a, b) || pathContains(b, a)
}

func pathContains(parent, child string) bool {
	rel, err := resolveRelPath(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (c *Client) executableName() string {
	if c.platform == PlatformWindows {
		return "steamcmd.exe"
	}
	return "steamcmd.sh"
}

func (c *Client) executablePath() string {
	return filepath.Join(c.steamCMDDir, c.executableName())
}

// ExecutablePath returns the path to the configured SteamCMD executable.
func (c *Client) ExecutablePath() string {
	return c.executablePath()
}

// Install installs SteamCMD if its platform entrypoint is not already a
// regular executable file.
func (c *Client) Install(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.installLocked(ctx)
}

func (c *Client) installLocked(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	valid, err := c.validEntrypoint()
	if err != nil {
		return err
	}
	if valid {
		return nil
	}
	if err := ensureInstallDirectory(c.steamCMDDir); err != nil {
		return fmt.Errorf("prepare SteamCMDDir: %w", err)
	}

	archivePath, err := prepareInstallArchive(c.steamCMDDir)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(archivePath) }()
	if err := installDownload(c, ctx, archivePath); err != nil {
		return fmt.Errorf("download SteamCMD: %w", err)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}

	parent := filepath.Dir(c.steamCMDDir)
	staging, err := installMkdirTemp(parent, ".steamcmd-staging-*")
	if err != nil {
		return fmt.Errorf("create installation staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if err := installExtract(archivePath, staging, c.platform); err != nil {
		return fmt.Errorf("extract SteamCMD: %w", err)
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	entrypoint := filepath.Join(staging, c.executableName())
	if err := validateExtractedEntrypoint(entrypoint, c.executableName()); err != nil {
		return err
	}
	if c.platform == PlatformLinux {
		if err := installChmod(entrypoint, 0o755); err != nil {
			return fmt.Errorf("make SteamCMD executable: %w", err)
		}
	}
	if err := c.publish(staging); err != nil {
		return fmt.Errorf("publish SteamCMD installation: %w", err)
	}
	return nil
}

func prepareInstallArchive(dir string) (string, error) {
	archive, err := createInstallTemp(dir, ".steamcmd-archive-*")
	if err != nil {
		return "", fmt.Errorf("create download temporary file: %w", err)
	}
	archivePath := archive.Name()
	if err := archive.Close(); err != nil {
		_ = installRemove(archivePath)
		return "", fmt.Errorf("close download temporary file: %w", err)
	}
	if err := installRemove(archivePath); err != nil {
		return "", fmt.Errorf("prepare download temporary file: %w", err)
	}
	return archivePath, nil
}

func validateExtractedEntrypoint(path, name string) error {
	info, err := installLstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("extracted archive does not contain %s", name)
		}
		return fmt.Errorf("inspect extracted entrypoint: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("extracted entrypoint %s is not a regular file", name)
	}
	return nil
}

func (c *Client) validEntrypoint() (bool, error) {
	if info, err := installLstat(c.steamCMDDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("SteamCMDDir is a symlink")
		}
		if !info.IsDir() {
			return false, errors.New("SteamCMDDir is not a directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("inspect SteamCMDDir: %w", err)
	}
	info, err := installLstat(c.executablePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect SteamCMD entrypoint: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, nil
	}
	if c.platform == PlatformLinux && runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
		return false, nil
	}
	return true, nil
}

func ensureInstallDirectory(dir string) error {
	if info, err := installLstat(dir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("SteamCMDDir is not a real directory")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return installMkdirAll(dir, 0o755)
}

func (c *Client) publish(staging string) error {
	parent := filepath.Dir(c.steamCMDDir)
	old, err := installLstat(c.steamCMDDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && old.Mode()&os.ModeSymlink != 0 {
		return errors.New("SteamCMDDir is a symlink")
	}
	if errors.Is(err, os.ErrNotExist) {
		return installRename(staging, c.steamCMDDir)
	}

	backup, err := installMkdirTemp(parent, ".steamcmd-old-*")
	if err != nil {
		return err
	}
	if err := installRemove(backup); err != nil {
		return err
	}
	if err := installRename(c.steamCMDDir, backup); err != nil {
		return err
	}
	if err := installRename(staging, c.steamCMDDir); err != nil {
		_ = installRename(backup, c.steamCMDDir)
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}

// Run executes SteamCMD directly without invoking a shell.
func (c *Client) Run(ctx context.Context, args ...string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runLocked(ctx, args...)
}

func (c *Client) runLocked(ctx context.Context, args ...string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("nil context")
	}
	runCtx := ctx
	cancel := func() {}
	if c.commandTimeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, c.commandTimeout)
	}
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return nil, err
	}

	var output []byte
	var err error
	collectorTruncated := false
	if c.commandRunner != nil {
		output, err = c.commandRunner(runCtx, c.executablePath(), append([]string(nil), args...))
	} else {
		cmd := exec.CommandContext(runCtx, c.executablePath(), args...)
		cmd.Dir = c.steamCMDDir
		collector := &boundedOutput{limit: c.maxOutputBytes}
		cmd.Stdout = collector
		cmd.Stderr = collector
		err = cmd.Run()
		output = collector.Bytes()
		collectorTruncated = collector.truncated
	}
	if runCtx.Err() != nil {
		err = runCtx.Err()
	}
	output, truncated := boundBytes(output, c.maxOutputBytes)
	truncated = truncated || collectorTruncated
	if err != nil {
		return output, &CommandError{
			Output:    output,
			Truncated: truncated,
			Err:       err,
			secrets:   []string{c.login.Password, c.login.GuardCode},
		}
	}
	return output, nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return errors.New("nil context")
	}
	return ctx.Err()
}

// Setup installs SteamCMD and then performs the requested app update.
func (c *Client) Setup(ctx context.Context, options UpdateOptions) (UpdateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateAppID(options.AppID); err != nil {
		return UpdateResult{AppID: options.AppID}, err
	}
	if err := c.installLocked(ctx); err != nil {
		return UpdateResult{AppID: options.AppID}, err
	}
	return c.updateLocked(ctx, options)
}

// Update performs an app update using an already installed SteamCMD.
func (c *Client) Update(ctx context.Context, options UpdateOptions) (UpdateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.updateLocked(ctx, options)
}

func (c *Client) updateLocked(ctx context.Context, options UpdateOptions) (UpdateResult, error) {
	result := UpdateResult{AppID: options.AppID}
	if err := validateAppID(options.AppID); err != nil {
		return result, err
	}
	if options.BetaPassword != "" && options.Beta == "" {
		return result, errors.New("BetaPassword requires Beta")
	}
	if err := contextErr(ctx); err != nil {
		return result, err
	}
	previous, previousErr := c.installedBuildIDLocked(options.AppID)
	if previousErr != nil {
		return result, previousErr
	}
	result.PreviousBuildID = previous

	comparison, latest, upToDate, err := c.compareBuilds(ctx, options, previous)
	if err != nil {
		return result, err
	}
	result.LatestBuildID = latest
	if upToDate {
		result.InstalledBuildID = previous
		result.Verified = true
		return result, nil
	}

	args := c.updateArgs(options)
	if _, err := c.runLocked(ctx, args...); err != nil {
		var commandErr *CommandError
		if errors.As(err, &commandErr) && options.BetaPassword != "" {
			commandErr.secrets = append(commandErr.secrets, options.BetaPassword)
		}
		return result, err
	}
	result.Updated = true
	installed, err := c.installedBuildIDLocked(options.AppID)
	if err != nil {
		return result, fmt.Errorf("read installed build ID for app %s: %w", options.AppID, err)
	}
	result.InstalledBuildID = installed
	if installed == "" {
		return result, fmt.Errorf("app %s update completed but installed build ID is empty", options.AppID)
	}
	if comparison && result.LatestBuildID != installed {
		return result, fmt.Errorf("app %s update installed build %s, expected public build %s", options.AppID, installed, result.LatestBuildID)
	}
	result.Verified = true
	return result, nil
}

func (c *Client) compareBuilds(ctx context.Context, options UpdateOptions, previous string) (bool, string, bool, error) {
	comparison := !options.Force && !options.Validate && options.Beta == ""
	if !comparison {
		return false, "", false, nil
	}
	latest, err := c.latestBuildIDLocked(ctx, options.AppID)
	if err != nil {
		return true, "", false, err
	}
	return true, latest, previous != "" && previous == latest, nil
}

func (c *Client) updateArgs(options UpdateOptions) []string {
	args := c.loginArgs()
	args = append(args,
		"+force_install_dir", c.installDir,
		"+@sSteamCmdForcePlatformType", string(c.platform),
		"+app_update", options.AppID,
	)
	if options.Beta != "" {
		args = append(args, "-beta", options.Beta)
		if options.BetaPassword != "" {
			args = append(args, "-betapassword", options.BetaPassword)
		}
	}
	if options.Validate {
		args = append(args, "validate")
	}
	args = append(args, options.ExtraArgs...)
	return append(args, "+quit")
}

func (c *Client) loginArgs() []string {
	if c.login.Username == "" {
		return []string{"+login", "anonymous"}
	}
	args := []string{"+login", c.login.Username, c.login.Password}
	if c.login.GuardCode != "" {
		args = append(args, c.login.GuardCode)
	}
	return args
}

// AppInfo fetches raw app information from SteamCMD.
func (c *Client) AppInfo(ctx context.Context, appID string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateAppID(appID); err != nil {
		return nil, err
	}
	return c.appInfoLocked(ctx, appID)
}

func (c *Client) appInfoLocked(ctx context.Context, appID string) ([]byte, error) {
	args := c.loginArgs()
	args = append(args, "+app_info_update", "1", "+app_info_print", appID, "+quit")
	return c.runLocked(ctx, args...)
}

// InstalledBuildID reads the app manifest from InstallDir.
func (c *Client) InstalledBuildID(appID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateAppID(appID); err != nil {
		return "", err
	}
	return c.installedBuildIDLocked(appID)
}

func (c *Client) installedBuildIDLocked(appID string) (string, error) {
	if info, err := installLstat(c.installDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("InstallDir is not a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect InstallDir: %w", err)
	}
	manifest := filepath.Join(c.installDir, "steamapps", "appmanifest_"+appID+".acf")
	data, err := readInstallFile(manifest)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	buildID, err := ParseBuildID(string(data))
	if err != nil {
		// Invalid manifests have the same observable state as missing manifests.
		return "", nil //nolint:nilerr // Invalid manifests are treated as absent.
	}
	return buildID, nil
}

// LatestBuildID fetches app information and parses its public build ID.
func (c *Client) LatestBuildID(ctx context.Context, appID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateAppID(appID); err != nil {
		return "", err
	}
	return c.latestBuildIDLocked(ctx, appID)
}

func (c *Client) latestBuildIDLocked(ctx context.Context, appID string) (string, error) {
	output, err := c.appInfoLocked(ctx, appID)
	if err != nil {
		return "", err
	}
	return ParseBuildID(string(output))
}

// CheckForUpdate compares the installed manifest with the public build.
func (c *Client) CheckForUpdate(ctx context.Context, appID string) (UpdateStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	status := UpdateStatus{AppID: appID}
	if err := validateAppID(appID); err != nil {
		return status, err
	}
	if err := contextErr(ctx); err != nil {
		return status, err
	}
	installed, err := c.installedBuildIDLocked(appID)
	if err != nil {
		return status, err
	}
	latest, err := c.latestBuildIDLocked(ctx, appID)
	if err != nil {
		return status, err
	}
	status.InstalledBuildID = installed
	status.PreviousBuildID = installed
	status.LatestBuildID = latest
	status.UpdateAvailable = installed == "" || installed != latest
	return status, nil
}

// DownloadWorkshopItem downloads one Steam Workshop item.
func (c *Client) DownloadWorkshopItem(ctx context.Context, options WorkshopDownloadOptions) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := validateAppID(options.AppID); err != nil {
		return err
	}
	if err := validateNumericID(options.PublishedFileID, 1, 20, "PublishedFileID"); err != nil {
		return err
	}
	args := c.loginArgs()
	args = append(args,
		"+@sSteamCmdForcePlatformType", string(c.platform),
		"+workshop_download_item", options.AppID, options.PublishedFileID,
	)
	if options.Validate {
		args = append(args, "validate")
	}
	args = append(args, "+quit")
	_, err := c.runLocked(ctx, args...)
	return err
}

func validateAppID(appID string) error {
	return validateNumericID(appID, 1, 10, "AppID")
}

func validateNumericID(value string, min, max int, name string) error {
	if len(value) < min || len(value) > max {
		return fmt.Errorf("%s must be %d-%d digits", name, min, max)
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return fmt.Errorf("%s must be %d-%d digits", name, min, max)
		}
	}
	return nil
}
