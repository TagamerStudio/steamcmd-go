package steamcmd

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(t *testing.T, runner CommandRunner) Config {
	t.Helper()
	root := t.TempDir()
	return Config{
		SteamCMDDir:    filepath.Join(root, "steamcmd"),
		InstallDir:     filepath.Join(root, "install"),
		Platform:       PlatformLinux,
		CommandRunner:  runner,
		Attempts:       1,
		CommandTimeout: time.Second,
	}
}

func TestNewConfig(t *testing.T) {
	root := t.TempDir()
	client, err := New(Config{
		SteamCMDDir: filepath.Join(root, "steamcmd"),
		InstallDir:  filepath.Join(root, "install"),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPlatform := Platform(runtime.GOOS)
	if client.platform != wantPlatform {
		t.Fatalf("platform = %q, want %q", client.platform, wantPlatform)
	}
	if len(client.downloadURLs) != 3 {
		t.Fatalf("download URL count = %d, want 3", len(client.downloadURLs))
	}
	if client.maxOutputBytes != defaultMaxOutputBytes {
		t.Fatalf("max output = %d, want %d", client.maxOutputBytes, defaultMaxOutputBytes)
	}
	if client.retryBackoff != defaultRetryBackoff {
		t.Fatalf("retry backoff = %s, want %s", client.retryBackoff, defaultRetryBackoff)
	}

	for _, platform := range []Platform{"darwin", "freebsd"} {
		_, err := New(Config{
			SteamCMDDir: filepath.Join(root, "one"),
			InstallDir:  filepath.Join(root, "two"),
			Platform:    platform,
		})
		if err == nil {
			t.Errorf("platform %q was accepted", platform)
		}
	}
	for _, dirs := range [][2]string{
		{filepath.Join(root, "same"), filepath.Join(root, "same")},
		{filepath.Join(root, "parent"), filepath.Join(root, "parent", "child")},
		{filepath.Join(root, "other", "child"), filepath.Join(root, "other")},
	} {
		_, err := New(Config{SteamCMDDir: dirs[0], InstallDir: dirs[1]})
		if err == nil {
			t.Errorf("overlapping paths %q and %q were accepted", dirs[0], dirs[1])
		}
	}
}

func TestNewValidationAndErrorHelpers(t *testing.T) {
	root := t.TempDir()
	base := Config{
		SteamCMDDir: filepath.Join(root, "steamcmd"),
		InstallDir:  filepath.Join(root, "install"),
		Platform:    PlatformLinux,
	}
	for _, test := range []struct {
		name   string
		config Config
	}{
		{"empty SteamCMDDir", Config{InstallDir: base.InstallDir, Platform: PlatformLinux}},
		{"empty InstallDir", Config{SteamCMDDir: base.SteamCMDDir, Platform: PlatformLinux}},
		{"negative attempts", func() Config { c := base; c.Attempts = -1; return c }()},
		{"negative retry backoff", func() Config { c := base; c.RetryBackoff = -1; return c }()},
		{"negative command timeout", func() Config { c := base; c.CommandTimeout = -1; return c }()},
		{"negative download timeout", func() Config { c := base; c.DownloadTimeout = -1; return c }()},
		{"negative output limit", func() Config { c := base; c.MaxOutputBytes = -1; return c }()},
		{"password without username", func() Config { c := base; c.Login.Password = "secret"; return c }()},
		{"guard code without username", func() Config { c := base; c.Login.GuardCode = "123"; return c }()},
		{"username without password", func() Config { c := base; c.Login.Username = "alice"; return c }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}

	windows := base
	windows.Platform = PlatformWindows
	client, err := New(windows)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(client.ExecutablePath(), "steamcmd.exe") || !strings.Contains(client.downloadURLs[0], "steamcmd.zip") {
		t.Fatalf("Windows client = %q, URLs = %#v", client.ExecutablePath(), client.downloadURLs)
	}

	oldAbs := resolveAbsPath
	t.Cleanup(func() { resolveAbsPath = oldAbs })
	resolveAbsPath = func(string) (string, error) { return "", errors.New("absolute path failure") }
	if _, err := New(base); err == nil || !strings.Contains(err.Error(), "SteamCMDDir") {
		t.Fatalf("SteamCMDDir resolution error = %v", err)
	}
	resolveAbsPath = func(path string) (string, error) {
		if path == base.InstallDir {
			return "", errors.New("absolute path failure")
		}
		return oldAbs(path)
	}
	if _, err := New(base); err == nil || !strings.Contains(err.Error(), "InstallDir") {
		t.Fatalf("InstallDir resolution error = %v", err)
	}
	resolveAbsPath = oldAbs

	oldRel := resolveRelPath
	t.Cleanup(func() { resolveRelPath = oldRel })
	resolveRelPath = func(string, string) (string, error) { return "", errors.New("relative path failure") }
	if pathContains("one", "two") {
		t.Fatal("relative path failure was treated as containment")
	}
	resolveRelPath = oldRel

	if contextErr(nilContext()) == nil {
		t.Fatal("nil context was accepted")
	}
	var nilCommandError *CommandError
	if nilCommandError.Error() != "steamcmd command failed" || nilCommandError.Unwrap() != nil {
		t.Fatal("nil CommandError behavior changed")
	}
	if (&CommandError{}).Error() != "steamcmd command failed" {
		t.Fatal("empty CommandError behavior changed")
	}
	underlying := errors.New("failed with secret")
	commandError := &CommandError{Err: underlying, secrets: []string{"secret"}}
	if commandError.Error() != "steamcmd command failed: failed with [redacted]" || !errors.Is(commandError, underlying) {
		t.Fatalf("CommandError = %q", commandError)
	}
}

func TestParseBuildID(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{"vdf", `"buildid" "123456"`, "123456"},
		{"colon", "BuildID : 987", "987"},
		{"equals", "buildid=\"42\"", "42"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseBuildID(test.output)
			if err != nil || got != test.want {
				t.Fatalf("ParseBuildID() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := ParseBuildID("no build here"); err == nil {
		t.Fatal("missing build ID was accepted")
	}
}

func TestGeneratedArgs(t *testing.T) {
	var mu sync.Mutex
	var calls [][]string
	runner := func(_ context.Context, executable string, args []string) ([]byte, error) {
		if !strings.HasSuffix(executable, "steamcmd.sh") {
			t.Errorf("executable = %q", executable)
		}
		mu.Lock()
		calls = append(calls, append([]string(nil), args...))
		mu.Unlock()
		return []byte(`"buildid" "99"`), nil
	}
	config := testConfig(t, runner)
	config.Login = Login{Username: "alice", Password: "secret", GuardCode: "123456"}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.AppInfo(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	if err := client.DownloadWorkshopItem(context.Background(), WorkshopDownloadOptions{
		AppID: "123", PublishedFileID: "456", Validate: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("runner calls = %d, want 2", len(calls))
	}
	wantInfo := []string{"+login", "alice", "secret", "123456", "+app_info_update", "1", "+app_info_print", "123", "+quit"}
	if !sameStrings(calls[0], wantInfo) {
		t.Fatalf("app info args = %#v, want %#v", calls[0], wantInfo)
	}
	wantWorkshop := []string{"+login", "alice", "secret", "123456", "+@sSteamCmdForcePlatformType", "linux", "+workshop_download_item", "123", "456", "validate", "+quit"}
	if !sameStrings(calls[1], wantWorkshop) {
		t.Fatalf("workshop args = %#v, want %#v", calls[1], wantWorkshop)
	}
}

func TestUpdateSkipForceValidateAndBeta(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	writeManifest(t, installDir, "123", "10")
	var mu sync.Mutex
	var calls [][]string
	runner := func(_ context.Context, _ string, args []string) ([]byte, error) {
		mu.Lock()
		calls = append(calls, append([]string(nil), args...))
		mu.Unlock()
		if contains(args, "+app_info_print") {
			return []byte(`"buildid" "10"`), nil
		}
		writeManifest(t, installDir, "123", "20")
		return []byte("updated"), nil
	}
	config := Config{
		SteamCMDDir:    filepath.Join(root, "steamcmd"),
		InstallDir:     installDir,
		Platform:       PlatformLinux,
		CommandRunner:  runner,
		Attempts:       1,
		CommandTimeout: time.Second,
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}

	result, err := client.Update(context.Background(), UpdateOptions{AppID: "123"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Updated || !result.Verified || result.PreviousBuildID != "10" || result.LatestBuildID != "10" {
		t.Fatalf("skip result = %+v", result)
	}
	if len(calls) != 1 {
		t.Fatalf("skip runner calls = %d, want 1", len(calls))
	}

	result, err = client.Update(context.Background(), UpdateOptions{AppID: "123", Force: true, ExtraArgs: []string{"-foo", "bar"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || !result.Verified || result.InstalledBuildID != "20" || len(calls) != 2 {
		t.Fatalf("force result = %+v, calls = %d", result, len(calls))
	}
	if !contains(calls[1], "-foo") || !contains(calls[1], "+force_install_dir") || !contains(calls[1], "linux") {
		t.Fatalf("force args = %#v", calls[1])
	}

	writeManifest(t, installDir, "123", "20")
	result, err = client.Update(context.Background(), UpdateOptions{AppID: "123", Validate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || !result.Verified || len(calls) != 3 || contains(calls[2], "+app_info_print") || !contains(calls[2], "validate") {
		t.Fatalf("validate result = %+v, args = %#v", result, calls[2])
	}

	writeManifest(t, installDir, "123", "30")
	result, err = client.Update(context.Background(), UpdateOptions{
		AppID: "123", Beta: "test", BetaPassword: "beta-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Updated || !result.Verified || result.LatestBuildID != "" || !contains(calls[3], "-beta") || !contains(calls[3], "-betapassword") {
		t.Fatalf("beta result = %+v, args = %#v", result, calls[3])
	}
}

func TestUpdateVerificationFailureCarriesResult(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	writeManifest(t, installDir, "123", "1")
	runner := func(_ context.Context, _ string, args []string) ([]byte, error) {
		if contains(args, "+app_info_print") {
			return []byte(`"buildid" "2"`), nil
		}
		writeManifest(t, installDir, "123", "3")
		return nil, nil
	}
	config := Config{
		SteamCMDDir:   filepath.Join(root, "steamcmd"),
		InstallDir:    installDir,
		Platform:      PlatformLinux,
		CommandRunner: runner,
		Attempts:      1,
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Update(context.Background(), UpdateOptions{AppID: "123"})
	if err == nil || result.InstalledBuildID != "3" || result.Verified {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestCheckForUpdateAndInstalledBuildID(t *testing.T) {
	root := t.TempDir()
	installDir := filepath.Join(root, "install")
	writeManifest(t, installDir, "123", "10")
	client, err := New(Config{
		SteamCMDDir: filepath.Join(root, "steamcmd"),
		InstallDir:  installDir,
		Platform:    PlatformLinux,
		CommandRunner: func(_ context.Context, _ string, _ []string) ([]byte, error) {
			return []byte(`"buildid" "11"`), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.CheckForUpdate(context.Background(), "123")
	if err != nil {
		t.Fatal(err)
	}
	if status.PreviousBuildID != "10" || status.InstalledBuildID != "10" || status.LatestBuildID != "11" || !status.UpdateAvailable {
		t.Fatalf("status = %+v", status)
	}
	installed, err := client.InstalledBuildID("123")
	if err != nil || installed != "10" {
		t.Fatalf("installed build = %q, %v", installed, err)
	}
}

func TestRunTimeoutOutputAndSecretRedaction(t *testing.T) {
	secret := "top-secret"
	config := testConfig(t, func(ctx context.Context, _ string, _ []string) ([]byte, error) {
		<-ctx.Done()
		return []byte(strings.Repeat("x", 100)), errors.New(secret)
	})
	config.CommandTimeout = 20 * time.Millisecond
	config.MaxOutputBytes = 10
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	output, err := client.Run(context.Background(), "+quit")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline exceeded", err)
	}
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error type = %T, want CommandError", err)
	}
	if len(output) != 10 || len(commandErr.Output) != 10 || !commandErr.Truncated || strings.Contains(err.Error(), secret) {
		t.Fatalf("output = %d, command error = %+v, error text = %q", len(output), commandErr, err.Error())
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Run(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Run error = %v", err)
	}
}

func TestRunAndPublicAPIErrorPaths(t *testing.T) {
	client, err := New(testConfig(t, func(_ context.Context, _ string, _ []string) ([]byte, error) {
		return nil, errors.New("runner failure")
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Run(nilContext()); err == nil {
		t.Fatal("nil Run context was accepted")
	}
	if _, err := client.Run(context.Background()); err == nil {
		t.Fatal("runner failure was ignored")
	}
	if _, err := client.AppInfo(context.Background(), "bad"); err == nil {
		t.Fatal("invalid AppInfo ID was accepted")
	}
	if _, err := client.InstalledBuildID("bad"); err == nil {
		t.Fatal("invalid InstalledBuildID ID was accepted")
	}
	if _, err := client.LatestBuildID(context.Background(), "bad"); err == nil {
		t.Fatal("invalid LatestBuildID ID was accepted")
	}
	if err := client.DownloadWorkshopItem(context.Background(), WorkshopDownloadOptions{AppID: "bad", PublishedFileID: "1"}); err == nil {
		t.Fatal("invalid workshop app ID was accepted")
	}
	if err := client.DownloadWorkshopItem(context.Background(), WorkshopDownloadOptions{AppID: "1", PublishedFileID: "bad"}); err == nil {
		t.Fatal("invalid workshop file ID was accepted")
	}
	if err := client.DownloadWorkshopItem(context.Background(), WorkshopDownloadOptions{AppID: "1", PublishedFileID: "2"}); err == nil {
		t.Fatal("workshop runner failure was ignored")
	}

	outputClient, err := New(Config{
		SteamCMDDir:    filepath.Join(t.TempDir(), "steamcmd"),
		InstallDir:     filepath.Join(t.TempDir(), "install"),
		Platform:       PlatformLinux,
		MaxOutputBytes: 3,
		CommandTimeout: 0,
		CommandRunner: func(context.Context, string, []string) ([]byte, error) {
			return []byte("12345"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if output, err := outputClient.Run(context.Background()); err != nil || string(output) != "123" {
		t.Fatalf("bounded successful Run() = %q, %v", output, err)
	}

	missingClient, err := New(Config{
		SteamCMDDir: filepath.Join(t.TempDir(), "steamcmd"),
		InstallDir:  filepath.Join(t.TempDir(), "install"),
		Platform:    PlatformLinux,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := missingClient.Run(context.Background()); err == nil {
		t.Fatal("missing executable was accepted")
	}
}

func TestRunDirectAndBoundedCombinedOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the direct-run fixture is a POSIX shell script")
	}
	root := t.TempDir()
	steamDir := filepath.Join(root, "steamcmd")
	installDir := filepath.Join(root, "install")
	if err := os.MkdirAll(steamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '1234567890abcdefghij'\nprintf 'stderr' >&2\nexit 7\n"
	path := filepath.Join(steamDir, "steamcmd.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	client, err := New(Config{
		SteamCMDDir:    steamDir,
		InstallDir:     installDir,
		Platform:       PlatformLinux,
		MaxOutputBytes: 10,
		CommandTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := client.Run(context.Background(), "argument with spaces")
	if err == nil {
		t.Fatal("successful command expected to fail")
	}
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || len(output) != 10 || !commandErr.Truncated {
		t.Fatalf("output = %q, error = %+v", output, err)
	}
}

func TestEntrypointAndInstallDirectoryChecks(t *testing.T) {
	root := t.TempDir()
	steamDir := filepath.Join(root, "steamcmd")
	installDir := filepath.Join(root, "install")
	client, err := New(Config{SteamCMDDir: steamDir, InstallDir: installDir, Platform: PlatformLinux})
	if err != nil {
		t.Fatal(err)
	}
	if valid, err := client.validEntrypoint(); err != nil || valid {
		t.Fatalf("missing entrypoint = %t, %v", valid, err)
	}
	if err := os.MkdirAll(steamDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entrypoint := client.executablePath()
	if err := os.WriteFile(entrypoint, []byte("script"), 0o644); err != nil {
		t.Fatal(err)
	}
	if valid, err := client.validEntrypoint(); err != nil || valid {
		t.Fatalf("non-executable entrypoint = %t, %v", valid, err)
	}
	if err := os.Remove(entrypoint); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(entrypoint, 0o755); err != nil {
		t.Fatal(err)
	}
	if valid, err := client.validEntrypoint(); err != nil || valid {
		t.Fatalf("directory entrypoint = %t, %v", valid, err)
	}
	if err := os.Remove(entrypoint); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypoint, []byte("script"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(entrypoint, 0o755); err != nil {
		t.Fatal(err)
	}
	if valid, err := client.validEntrypoint(); runtime.GOOS != "windows" && (err != nil || !valid) {
		t.Fatalf("executable entrypoint = %t, %v", valid, err)
	}
	if err := ensureInstallDirectory(installDir); err != nil {
		t.Fatal(err)
	}
	if err := ensureInstallDirectory(installDir); err != nil {
		t.Fatal(err)
	}
	fileDir := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(fileDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ensureInstallDirectory(fileDir); err == nil {
		t.Fatal("file install directory was accepted")
	}
	oldLstat := installLstat
	t.Cleanup(func() { installLstat = oldLstat })
	installLstat = func(string) (os.FileInfo, error) { return nil, errors.New("lstat failure") }
	if _, err := client.validEntrypoint(); err == nil {
		t.Fatal("SteamCMDDir lstat failure was ignored")
	}
	if err := ensureInstallDirectory(installDir); err == nil {
		t.Fatal("install directory lstat failure was ignored")
	}
	installLstat = oldLstat
	installLstat = func(path string) (os.FileInfo, error) {
		if path == client.executablePath() {
			return nil, errors.New("entrypoint lstat failure")
		}
		return oldLstat(path)
	}
	if _, err := client.validEntrypoint(); err == nil {
		t.Fatal("entrypoint lstat failure was ignored")
	}
	installLstat = oldLstat

	fileSteamDir := filepath.Join(root, "file-steam")
	if err := os.WriteFile(fileSteamDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileClient, err := New(Config{SteamCMDDir: fileSteamDir, InstallDir: filepath.Join(root, "file-install"), Platform: PlatformLinux})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileClient.validEntrypoint(); err == nil {
		t.Fatal("file SteamCMDDir was accepted")
	}
	if err := fileClient.Install(context.Background()); err == nil {
		t.Fatal("file SteamCMDDir install was accepted")
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "steam-link")
		if err := os.Symlink(steamDir, link); err != nil {
			t.Fatal(err)
		}
		linkClient, err := New(Config{SteamCMDDir: link, InstallDir: filepath.Join(root, "link-install"), Platform: PlatformLinux})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := linkClient.validEntrypoint(); err == nil {
			t.Fatal("symlink SteamCMDDir was accepted")
		}
	}
}

func TestInstallLifecycleErrorsAndSetup(t *testing.T) {
	root := t.TempDir()
	archiveData := makeTarGz(t, tarEntry{name: "steamcmd.sh", typeFlag: tar.TypeReg, data: "#!/bin/sh\n"})
	config := Config{
		SteamCMDDir:  filepath.Join(root, "steamcmd"),
		InstallDir:   filepath.Join(root, "install"),
		Platform:     PlatformLinux,
		Attempts:     1,
		DownloadURLs: []string{"archive"},
		Downloader: func(_ context.Context, _, path string) error {
			return os.WriteFile(path, archiveData, 0o644)
		},
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.Install(nilContext()); err == nil {
		t.Fatal("nil install context was accepted")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.Install(canceled); err == nil {
		t.Fatal("canceled install context was accepted")
	}

	oldCreate := createInstallTemp
	oldRemove := installRemove
	oldMkdirTemp := installMkdirTemp
	oldDownload := installDownload
	oldExtract := installExtract
	oldLstat := installLstat
	oldChmod := installChmod
	oldRename := installRename
	t.Cleanup(func() {
		createInstallTemp = oldCreate
		installRemove = oldRemove
		installMkdirTemp = oldMkdirTemp
		installDownload = oldDownload
		installExtract = oldExtract
		installLstat = oldLstat
		installChmod = oldChmod
		installRename = oldRename
	})

	newClient := func(name string) *Client {
		client, err := New(Config{
			SteamCMDDir:  filepath.Join(root, name, "steamcmd"),
			InstallDir:   filepath.Join(root, name, "install"),
			Platform:     PlatformLinux,
			Attempts:     1,
			DownloadURLs: []string{"archive"},
			Downloader: func(_ context.Context, _, path string) error {
				return os.WriteFile(path, archiveData, 0o644)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return client
	}

	createInstallTemp = func(string, string) (installTempFile, error) {
		return nil, errors.New("archive create failure")
	}
	if err := newClient("create").Install(context.Background()); err == nil {
		t.Fatal("archive create failure was ignored")
	}
	createInstallTemp = func(string, string) (installTempFile, error) {
		return &fakeInstallTempFile{name: filepath.Join(root, "archive-close"), closeErr: errors.New("archive close failure")}, nil
	}
	if err := newClient("close").Install(context.Background()); err == nil {
		t.Fatal("archive close failure was ignored")
	}
	createInstallTemp = oldCreate
	installRemove = func(string) error { return errors.New("archive remove failure") }
	if err := newClient("remove").Install(context.Background()); err == nil {
		t.Fatal("archive remove failure was ignored")
	}
	installRemove = oldRemove

	downloadFailure := newClient("download")
	downloadFailure.downloader = func(context.Context, string, string) error { return errors.New("download failure") }
	if err := downloadFailure.Install(context.Background()); err == nil {
		t.Fatal("download failure was ignored")
	}
	installMkdirTemp = func(string, string) (string, error) { return "", errors.New("staging create failure") }
	if err := newClient("staging").Install(context.Background()); err == nil {
		t.Fatal("staging create failure was ignored")
	}
	installMkdirTemp = oldMkdirTemp

	badArchive := newClient("bad-archive")
	badArchive.downloader = func(_ context.Context, _, path string) error {
		return os.WriteFile(path, []byte("not archive"), 0o644)
	}
	if err := badArchive.Install(context.Background()); err == nil {
		t.Fatal("bad archive was accepted")
	}

	missingEntrypoint := newClient("missing-entrypoint")
	missingEntrypoint.downloader = func(_ context.Context, _, path string) error {
		return os.WriteFile(path, makeTarGz(t, tarEntry{name: "other", typeFlag: tar.TypeReg, data: "x"}), 0o644)
	}
	if err := missingEntrypoint.Install(context.Background()); err == nil {
		t.Fatal("archive without entrypoint was accepted")
	}

	nonRegular := newClient("non-regular-entrypoint")
	nonRegular.downloader = func(_ context.Context, _, path string) error {
		return os.WriteFile(path, makeTarGz(t, tarEntry{name: "steamcmd.sh", typeFlag: tar.TypeDir}), 0o644)
	}
	if err := nonRegular.Install(context.Background()); err == nil {
		t.Fatal("directory entrypoint was accepted")
	}

	installLstat = func(path string) (os.FileInfo, error) {
		if strings.Contains(path, ".steamcmd-staging-") {
			return nil, errors.New("entrypoint inspect failure")
		}
		return oldLstat(path)
	}
	if err := newClient("entrypoint-inspect").Install(context.Background()); err == nil {
		t.Fatal("entrypoint inspect failure was ignored")
	}
	installLstat = oldLstat
	ensureErrorClient := newClient("ensure-error")
	ensureLstatCalls := 0
	installLstat = func(path string) (os.FileInfo, error) {
		if path == ensureErrorClient.steamCMDDir {
			ensureLstatCalls++
			if ensureLstatCalls == 1 {
				return nil, os.ErrNotExist
			}
			return nil, errors.New("ensure lstat failure")
		}
		return oldLstat(path)
	}
	if err := ensureErrorClient.Install(context.Background()); err == nil {
		t.Fatal("ensure directory failure was ignored")
	}
	installLstat = oldLstat

	installChmod = func(string, os.FileMode) error { return errors.New("chmod failure") }
	if err := newClient("chmod").Install(context.Background()); err == nil {
		t.Fatal("chmod failure was ignored")
	}
	installChmod = oldChmod

	postDownloadContext, cancelPostDownload := context.WithCancel(context.Background())
	installDownload = func(_ *Client, _ context.Context, path string) error {
		if err := os.WriteFile(path, archiveData, 0o644); err != nil {
			return err
		}
		cancelPostDownload()
		return nil
	}
	if err := newClient("post-download-context").Install(postDownloadContext); err == nil {
		t.Fatal("post-download cancellation was ignored")
	}
	installDownload = oldDownload

	postExtractContext, cancelPostExtract := context.WithCancel(context.Background())
	installDownload = func(_ *Client, _ context.Context, path string) error {
		return os.WriteFile(path, []byte("unused"), 0o644)
	}
	installExtract = func(_ string, destination string, _ Platform) error {
		if err := os.WriteFile(filepath.Join(destination, "steamcmd.sh"), []byte("script"), 0o755); err != nil {
			return err
		}
		cancelPostExtract()
		return nil
	}
	if err := newClient("post-extract-context").Install(postExtractContext); err == nil {
		t.Fatal("post-extract cancellation was ignored")
	}
	installDownload = oldDownload
	installExtract = oldExtract

	publishFailure := newClient("publish-failure")
	publishFailure.downloader = func(_ context.Context, _, path string) error {
		return os.WriteFile(path, archiveData, 0o644)
	}
	installRename = func(string, string) error { return errors.New("publish failure") }
	if err := publishFailure.Install(context.Background()); err == nil {
		t.Fatal("publish failure was ignored")
	}
	installRename = oldRename

	setupRoot := t.TempDir()
	setupArchive := makeTarGz(t, tarEntry{name: "steamcmd.sh", typeFlag: tar.TypeReg, data: "script"})
	setupClient, err := New(Config{
		SteamCMDDir:  filepath.Join(setupRoot, "steamcmd"),
		InstallDir:   filepath.Join(setupRoot, "install"),
		Platform:     PlatformLinux,
		Attempts:     1,
		DownloadURLs: []string{"archive"},
		Downloader: func(_ context.Context, _, path string) error {
			return os.WriteFile(path, setupArchive, 0o644)
		},
		CommandRunner: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if contains(args, "+app_info_print") {
				return []byte(`"buildid" "7"`), nil
			}
			writeManifest(t, filepath.Join(setupRoot, "install"), "123", "7")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	failedSetup, err := New(Config{
		SteamCMDDir: filepath.Join(root, "failed-setup-steam"),
		InstallDir:  filepath.Join(root, "failed-setup-install"),
		Platform:    PlatformLinux,
	})
	if err != nil {
		t.Fatal(err)
	}
	failedSetupContext, cancelFailedSetup := context.WithCancel(context.Background())
	cancelFailedSetup()
	if _, err := failedSetup.Setup(failedSetupContext, UpdateOptions{AppID: "123"}); err == nil {
		t.Fatal("Setup install failure was ignored")
	}
	result, err := setupClient.Setup(context.Background(), UpdateOptions{AppID: "123"})
	if err != nil || !result.Updated || !result.Verified {
		t.Fatalf("Setup() = %+v, %v", result, err)
	}
	if _, err := setupClient.Setup(context.Background(), UpdateOptions{AppID: "bad"}); err == nil {
		t.Fatal("invalid Setup app ID was accepted")
	}
}

type fakeInstallTempFile struct {
	name     string
	closeErr error
}

func (f *fakeInstallTempFile) Name() string {
	return f.name
}

func (f *fakeInstallTempFile) Close() error {
	return f.closeErr
}

func TestPublishPaths(t *testing.T) {
	root := t.TempDir()
	newClient := func(name string) *Client {
		return &Client{steamCMDDir: filepath.Join(root, name, "steamcmd")}
	}
	oldLstat := installLstat
	oldRemove := installRemove
	oldMkdirTemp := installMkdirTemp
	oldRename := installRename
	t.Cleanup(func() {
		installLstat = oldLstat
		installRemove = oldRemove
		installMkdirTemp = oldMkdirTemp
		installRename = oldRename
	})

	client := newClient("new")
	if err := os.MkdirAll(filepath.Dir(client.steamCMDDir), 0o755); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(root, "new-staging")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := client.publish(staging); err != nil {
		t.Fatal(err)
	}

	existing := newClient("existing")
	if err := os.MkdirAll(existing.steamCMDDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing.steamCMDDir, "old"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	existingStaging := filepath.Join(root, "existing-staging")
	if err := os.MkdirAll(existingStaging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existingStaging, "new"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := existing.publish(existingStaging); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(existing.steamCMDDir, "new")); err != nil {
		t.Fatal(err)
	}

	installLstat = func(string) (os.FileInfo, error) { return nil, errors.New("publish lstat failure") }
	if err := newClient("lstat").publish(filepath.Join(root, "lstat-staging")); err == nil {
		t.Fatal("publish lstat failure was ignored")
	}
	installLstat = oldLstat

	if runtime.GOOS != "windows" {
		linkClient := newClient("link")
		if err := os.MkdirAll(filepath.Dir(linkClient.steamCMDDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "target"), linkClient.steamCMDDir); err != nil {
			t.Fatal(err)
		}
		if err := linkClient.publish(filepath.Join(root, "link-staging")); err == nil {
			t.Fatal("symlink publish destination was accepted")
		}
	}

	abortRename := newClient("rename-error")
	if err := os.MkdirAll(filepath.Dir(abortRename.steamCMDDir), 0o755); err != nil {
		t.Fatal(err)
	}
	abortStaging := filepath.Join(root, "rename-error-staging")
	if err := os.MkdirAll(abortStaging, 0o755); err != nil {
		t.Fatal(err)
	}
	installRename = func(string, string) error { return errors.New("publish rename failure") }
	if err := abortRename.publish(abortStaging); err == nil {
		t.Fatal("publish rename failure was ignored")
	}
	installRename = oldRename

	backupCreate := newClient("backup-create")
	if err := os.MkdirAll(backupCreate.steamCMDDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installMkdirTemp = func(string, string) (string, error) { return "", errors.New("backup create failure") }
	if err := backupCreate.publish(filepath.Join(root, "backup-create-staging")); err == nil {
		t.Fatal("backup create failure was ignored")
	}
	installMkdirTemp = oldMkdirTemp

	backupRemove := newClient("backup-remove")
	if err := os.MkdirAll(backupRemove.steamCMDDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installRemove = func(string) error { return errors.New("backup remove failure") }
	if err := backupRemove.publish(filepath.Join(root, "backup-remove-staging")); err == nil {
		t.Fatal("backup remove failure was ignored")
	}
	installRemove = oldRemove

	oldRenameClient := newClient("old-rename")
	if err := os.MkdirAll(oldRenameClient.steamCMDDir, 0o755); err != nil {
		t.Fatal(err)
	}
	installRename = func(string, string) error { return errors.New("old rename failure") }
	if err := oldRenameClient.publish(filepath.Join(root, "old-rename-staging")); err == nil {
		t.Fatal("old rename failure was ignored")
	}
	installRename = oldRename

	newRenameClient := newClient("new-rename")
	if err := os.MkdirAll(newRenameClient.steamCMDDir, 0o755); err != nil {
		t.Fatal(err)
	}
	newRenameStaging := filepath.Join(root, "new-rename-staging")
	if err := os.MkdirAll(newRenameStaging, 0o755); err != nil {
		t.Fatal(err)
	}
	renameCalls := 0
	installRename = func(old, new string) error {
		renameCalls++
		if renameCalls == 2 {
			return errors.New("new rename failure")
		}
		return os.Rename(old, new)
	}
	if err := newRenameClient.publish(newRenameStaging); err == nil {
		t.Fatal("new rename failure was ignored")
	}
}

func TestUpdateAndBuildIDErrorPaths(t *testing.T) {
	root := t.TempDir()
	client, err := New(Config{
		SteamCMDDir: filepath.Join(root, "steamcmd"),
		InstallDir:  filepath.Join(root, "install"),
		Platform:    PlatformLinux,
		CommandRunner: func(_ context.Context, _ string, _ []string) ([]byte, error) {
			return []byte(`"buildid" "4"`), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if installed, err := client.InstalledBuildID("123"); err != nil || installed != "" {
		t.Fatalf("missing manifest = %q, %v", installed, err)
	}
	if latest, err := client.LatestBuildID(context.Background(), "123"); err != nil || latest != "4" {
		t.Fatalf("latest build = %q, %v", latest, err)
	}
	if status, err := client.CheckForUpdate(context.Background(), "123"); err != nil || !status.UpdateAvailable || status.InstalledBuildID != "" {
		t.Fatalf("missing manifest status = %+v, %v", status, err)
	}

	invalidManifestDir := filepath.Join(root, "invalid-manifest")
	if err := os.MkdirAll(filepath.Join(invalidManifestDir, "steamapps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalidManifestDir, "steamapps", "appmanifest_123.acf"), []byte("invalid"), 0o644); err != nil {
		t.Fatal(err)
	}
	invalidManifestClient, err := New(Config{SteamCMDDir: filepath.Join(root, "invalid-steam"), InstallDir: invalidManifestDir, Platform: PlatformLinux})
	if err != nil {
		t.Fatal(err)
	}
	if installed, err := invalidManifestClient.InstalledBuildID("123"); err != nil || installed != "" {
		t.Fatalf("invalid manifest = %q, %v", installed, err)
	}

	oldReadInstallFile := readInstallFile
	t.Cleanup(func() { readInstallFile = oldReadInstallFile })
	readInstallFile = func(string) ([]byte, error) { return nil, errors.New("manifest read failure") }
	if _, err := invalidManifestClient.InstalledBuildID("123"); err == nil {
		t.Fatal("manifest read failure was ignored")
	}
	readInstallFile = oldReadInstallFile

	oldInstallLstat := installLstat
	t.Cleanup(func() { installLstat = oldInstallLstat })
	installLstat = func(path string) (os.FileInfo, error) {
		if path == invalidManifestClient.installDir {
			return nil, errors.New("install directory inspection failure")
		}
		return oldInstallLstat(path)
	}
	if _, err := invalidManifestClient.InstalledBuildID("123"); err == nil {
		t.Fatal("install directory inspection failure was ignored")
	}
	installLstat = oldInstallLstat

	fileInstallDir := filepath.Join(root, "file-install")
	if err := os.WriteFile(fileInstallDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fileClient, err := New(Config{SteamCMDDir: filepath.Join(root, "file-steam"), InstallDir: fileInstallDir, Platform: PlatformLinux, CommandRunner: func(context.Context, string, []string) ([]byte, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileClient.InstalledBuildID("123"); err == nil {
		t.Fatal("manifest read failure was ignored")
	}
	if _, err := fileClient.CheckForUpdate(context.Background(), "123"); err == nil {
		t.Fatal("CheckForUpdate manifest read failure was ignored")
	}
	if _, err := fileClient.Update(context.Background(), UpdateOptions{AppID: "123", Force: true}); err == nil {
		t.Fatal("Update manifest read failure was ignored")
	}

	if _, err := client.Update(nilContext(), UpdateOptions{AppID: "123"}); err == nil {
		t.Fatal("nil Update context was accepted")
	}
	if _, err := client.Update(context.Background(), UpdateOptions{AppID: "bad"}); err == nil {
		t.Fatal("invalid Update app ID was accepted")
	}
	if _, err := client.CheckForUpdate(context.Background(), "bad"); err == nil {
		t.Fatal("invalid CheckForUpdate app ID was accepted")
	}
	if _, err := client.Update(context.Background(), UpdateOptions{AppID: "123", BetaPassword: "password"}); err == nil {
		t.Fatal("beta password without beta was accepted")
	}

	latestErrorClient, err := New(Config{
		SteamCMDDir: filepath.Join(root, "latest-error-steam"),
		InstallDir:  filepath.Join(root, "latest-error-install"),
		Platform:    PlatformLinux,
		CommandRunner: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if contains(args, "+app_info_print") {
				return nil, errors.New("latest failure")
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := latestErrorClient.Update(context.Background(), UpdateOptions{AppID: "123"}); err == nil {
		t.Fatal("latest build failure was ignored")
	}
	if _, err := latestErrorClient.CheckForUpdate(context.Background(), "123"); err == nil {
		t.Fatal("CheckForUpdate latest build failure was ignored")
	}

	secret := "beta-secret"
	runErrorClient, err := New(Config{
		SteamCMDDir: filepath.Join(root, "run-error-steam"),
		InstallDir:  filepath.Join(root, "run-error-install"),
		Platform:    PlatformLinux,
		CommandRunner: func(_ context.Context, _ string, args []string) ([]byte, error) {
			if contains(args, "+app_info_print") {
				return []byte(`"buildid" "4"`), nil
			}
			return nil, errors.New(secret)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runErrorClient.Update(context.Background(), UpdateOptions{AppID: "123", Beta: "test", BetaPassword: secret})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("beta command error = %v", err)
	}

	emptyAfterUpdate, err := New(Config{
		SteamCMDDir:   filepath.Join(root, "empty-update-steam"),
		InstallDir:    filepath.Join(root, "empty-update-install"),
		Platform:      PlatformLinux,
		CommandRunner: func(context.Context, string, []string) ([]byte, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := emptyAfterUpdate.Update(context.Background(), UpdateOptions{AppID: "123", Force: true}); err == nil {
		t.Fatal("empty installed build ID was accepted")
	}

	postReadInstall := filepath.Join(root, "post-read-install")
	if err := os.MkdirAll(postReadInstall, 0o755); err != nil {
		t.Fatal(err)
	}
	postReadClient, err := New(Config{
		SteamCMDDir: filepath.Join(root, "post-read-steam"),
		InstallDir:  postReadInstall,
		Platform:    PlatformLinux,
		CommandRunner: func(context.Context, string, []string) ([]byte, error) {
			if err := os.RemoveAll(filepath.Join(postReadInstall, "steamapps")); err != nil {
				return nil, err
			}
			return nil, os.WriteFile(filepath.Join(postReadInstall, "steamapps"), []byte("file"), 0o644)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postReadClient.Update(context.Background(), UpdateOptions{AppID: "123", Force: true}); err == nil {
		t.Fatal("post-update manifest read failure was ignored")
	}

	if _, err := client.LatestBuildID(context.Background(), "123"); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.CheckForUpdate(canceled, "123"); err == nil {
		t.Fatal("canceled CheckForUpdate was accepted")
	}
}

func TestIDValidation(t *testing.T) {
	for _, id := range []string{"", "12345678901", "1x", "-1", " 1"} {
		if err := validateAppID(id); err == nil {
			t.Errorf("invalid app ID %q accepted", id)
		}
	}
	for _, id := range []string{"1", "1234567890"} {
		if err := validateAppID(id); err != nil {
			t.Errorf("valid app ID %q rejected: %v", id, err)
		}
	}
	if err := validateNumericID("123456789012345678901", 1, 20, "PublishedFileID"); err == nil {
		t.Fatal("overlong workshop ID accepted")
	}
}

func writeManifest(t *testing.T, installDir, appID, buildID string) {
	t.Helper()
	dir := filepath.Join(installDir, "steamapps")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte("\"AppState\" {\n\t\"appid\" \"" + appID + "\"\n\t\"buildid\" \"" + buildID + "\"\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "appmanifest_"+appID+".acf"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func nilContext() context.Context {
	return nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
