# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- Open-source readiness: CODE_OF_CONDUCT.md, CHANGELOG.md, license badge.

### Changed
- Minimum Go version raised to 1.25.7.
- Upgraded `go-ipld-prime` from 0.22.0 to 0.24.0 (strict DAG-CBOR decode defaults).
- Upgraded `go.uber.org/zap` from 1.27.1 to 1.28.0.
- Upgraded `golang.org/x/sync` from 0.19.0 to 0.20.0.
- Upgraded `bits-and-blooms/bitset` from 1.7.0 to 1.24.4.

### Fixed
- Windows daemon no longer terminates when the parent console closes (#92).
