# Security Policy

## Supported Versions

Only the latest release line receives security fixes.

| Version | Supported          |
| ------- | ------------------ |
| 0.1.x   | :white_check_mark: |
| < 0.1   | :x:                |

## Reporting a Vulnerability

Please do not report security vulnerabilities in public issues.

Use GitHub's private vulnerability reporting form when it is enabled for this
repository:

<https://github.com/TagamerStudio/steamcmd-go/security/advisories/new>

If the form is unavailable, open an issue titled "Security contact request"
without any vulnerability details and a maintainer will provide a private
channel.

Include, when possible:

- the affected version of this library;
- the Go version and operating system;
- a minimal reproduction;
- the potential impact;
- relevant logs with credentials and personal data removed.

Never include Steam credentials, Steam Guard codes, downloaded server content or
other secrets in a report.

## Response

Reports are handled on a best-effort basis. We aim to acknowledge valid reports
within 3 business days and to coordinate disclosure after a fix is available.
Please allow time for a release before disclosing the issue publicly.

## Scope

This policy covers vulnerabilities in steamcmd-go, including:

- archive extraction (ZIP/TAR path traversal, absolute paths, symlinks and size
  limits);
- SteamCMD output and build ID parsing;
- download redirects, size limits and temporary-file handling;
- HTTP client configuration and atomic file replacement behavior.

The following are out of scope:

- vulnerabilities in SteamCMD or Steam itself; report those to Valve;
- server content distributed through SteamCMD;
- misconfiguration of the calling application, such as running the agent with
  elevated privileges or exposing downloaded files directly.

## Secure Usage

- The built-in downloader does not verify a published checksum. If you require
  SHA-256 verification, use a custom `Downloader` and verify the archive before
  returning it for extraction.
- Use dedicated `SteamCMDDir` and `InstallDir` paths and do not run the process
  as administrator/root.
- Keep the HTTP client timeout and redirect policy finite.
- Treat SteamCMD output as untrusted input; do not use it to build shell
  commands without validation.
