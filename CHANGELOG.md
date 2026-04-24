# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]
### Added
- `GET /portfolios/{slug}/holdings-impact` — per-ticker contribution to
  portfolio return across YTD, 1Y, 3Y, 5Y, inception with top-N + rest
  bucket. Requires pvbt v5 snapshots (schema v5 / `positions_daily`).
- Added a REST endpoint that creates a new Plaid link token

## [0.1.0] - 2024-12-14
### Added
- Tests for postgresql schema and functions

[0.0.0]: https://github.com/penny-vault/pv-api/releases/tag/v0.1.0
