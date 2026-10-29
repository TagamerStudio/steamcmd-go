# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.1.3] - 2026-10-29

### Security

- Add a security policy and enable private vulnerability reporting.
- Add CodeQL analysis and scheduled fuzzing for build ID and archive-name
  parsing.

### Changed

- Add weekly Dependabot updates for Go modules and GitHub Actions.
- Add a tag-driven release workflow that validates the changelog and tag before
  publishing.

## [0.1.2] - 2026-09-01

### Fixed

- Make Windows test fixtures portable.

## [0.1.1] - 2026-09-01

### Fixed

- Support native Windows client tests.

## [0.1.0] - 2026-09-01

### Added

- Initial SteamCMD client with app update, build ID parsing and archive
  extraction.
