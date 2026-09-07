# Changelog

All notable changes to Inroad are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

`Unreleased` collects what has landed on `main` since the last tag. Cutting a
release moves that block under a new `## [x.y.z] - YYYY-MM-DD` heading; the
release workflow reads the matching block for the GitHub Release body, so keep
the headings exactly `## [x.y.z] - YYYY-MM-DD`.

## [Unreleased]

### Added

- Release automation: tagged GitHub Releases with binaries for linux
  (amd64/arm64), macOS (arm64) and Windows (amd64), plus multi-arch container
  images published to the GitHub Container Registry for the api, worker and web
  services (`ghcr.io/<owner>/inroad-{api,worker,web}`). A push to `main`
  publishes a moving `edge` image tag.
- `internal/platform/version`: the running binary now reports its release tag,
  commit and build date on the startup log line.
- Supply-chain CI: `govulncheck`, `npm audit` and a Trivy filesystem scan on
  every PR and on a weekly schedule, plus Dependabot for Go modules, npm, GitHub
  Actions and the Dockerfiles.

### Changed

- `INROAD_REDIS_ADDR` now also accepts a full `redis://` / `rediss://` URL, so a
  managed Redis that needs a password, a non-default database, or TLS can be
  configured without code changes. A bare `host:port` keeps working unchanged,
  and a malformed URL is rejected at startup rather than mid-boot.
- `/readyz` now also checks Redis. A Redis outage previously reported ready even
  though every sign-in was failing closed at the rate limiter and every task
  enqueue was erroring.
