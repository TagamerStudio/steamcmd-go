# steamcmd-go

[![CI](https://github.com/TagamerStudio/steamcmd-go/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/TagamerStudio/steamcmd-go/actions/workflows/ci.yml)
[![codecov](https://codecov.io/gh/TagamerStudio/steamcmd-go/graph/badge.svg)](https://codecov.io/gh/TagamerStudio/steamcmd-go)

`steamcmd-go` is a standard-library-only Go client for installing and driving
SteamCMD on Linux and Windows.

## Install

```bash
go get github.com/TagamerStudio/steamcmd-go
```

## Usage

```go
package main

import (
	"context"
	"log"

	"github.com/TagamerStudio/steamcmd-go"
)

func main() {
	ctx := context.Background()
	client, err := steamcmd.New(steamcmd.Config{
		SteamCMDDir: "/var/lib/steamcmd",
		InstallDir:  "/var/lib/game",
	})
	if err != nil {
		log.Fatal(err)
	}

	result, err := client.Setup(ctx, steamcmd.UpdateOptions{
		AppID: "1234",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("updated=%t build=%s", result.Updated, result.InstalledBuildID)
}
```

An empty `Login.Username` uses anonymous login. Authenticated login is
configured explicitly with `Login{Username, Password, GuardCode}`. An empty
`Platform` uses the host platform; use `PlatformLinux` or `PlatformWindows`
for cross-platform provisioning.

`Setup` installs SteamCMD when necessary and updates the requested app.
`Install` and `Update` are available separately. `AppInfo`, build ID queries,
`CheckForUpdate`, generic `Run`, and Workshop downloads are also available.

### Update options

`UpdateOptions` supports beta branches, beta passwords, validation, forced
updates, and additional app-update arguments. App IDs and Workshop file IDs
must be numeric Steam IDs.

The public build comparison is skipped for beta updates, because the public
app-info build is not necessarily the build for a selected branch.

### Configuration

`Attempts`, `RetryBackoff`, `CommandTimeout`, `DownloadTimeout`, and
`MaxOutputBytes` have safe defaults and can be overridden. The default
installer tries three official download URLs, retries each mirror, downloads
atomically, and extracts through bounded ZIP/TAR.GZ readers.

`Downloader` and `CommandRunner` are available for tests and controlled
environments. The package has no external runtime dependencies.

### Security

SteamCMD is always started directly with `os/exec`; no shell is used. App IDs
are validated before becoming command arguments. Archive extraction rejects
absolute paths, traversal, links, special files, oversized entries, and
excessive entry counts. SteamCMD installation uses a staging directory and
does not replace a valid installation until extraction succeeds.

Operations on one `Client` are serialized. Applications using the same
directories from multiple processes must provide cross-process coordination.
The package does not log credentials or command arguments. SteamCMD passwords
may still be visible in the operating system's process list while a command is
running.

### Errors

Command failures are returned as `*CommandError`. Its bounded `Output` field
contains captured output for diagnostics, while `Error()` does not include
command arguments or output. Context cancellation and deadlines remain
detectable with `errors.Is`.

## Development

```bash
make check
```

The check runs the linter, tests with the race detector, and the strict 100%
statement coverage gate. Other useful targets are `make fmt`, `make tidy`, and
`make cover`; the latter writes `coverage.out` and prints the per-function
coverage report.

CI also runs `go vet`, scans dependencies with `govulncheck`, builds on Linux,
cross-builds for Windows, and runs the test suite natively on Windows.

## Coverage

```bash
make cover
```

Coverage is measured with atomic instrumentation and must remain at 100%
statement coverage. CI publishes push coverage reports to Codecov.

## License

BSD 3-Clause. See [LICENSE](LICENSE).
