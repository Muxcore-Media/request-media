# Changelog

## [0.3.0] — 2026-09-05

### Added

- Household approval loop: `pending` → `ApproveRequest` / `DenyRequest` with persisted `deny_reason`
- gRPC `ListRequests`, `ApproveRequest`, `DenyRequest`, `AddToWatchlist`, `RemoveFromWatchlist`
- Permission checks via mesh `authorizer` (`media.request` resource actions: create, list, approve, deny, watchlist)
- Auto-approve when caller has approve permission
- HTTP JSON approve/deny/watchlist endpoints and `GET /api/requests?status=`
- Setting / env `REQUEST_REQUIRE_APPROVAL` (default `true`)

### Fixed

- Authz fail-closed when authorizer module is unavailable (no longer allows approve/create/list)
- HTTP `GET /api/requests` gated with same `list` permission as gRPC `ListRequests`
- `GetStatus` requires `list` permission on `media.request`
- HTTP caller identity no longer defaults to spoofable `http-local`; missing `X-Caller-Id` is unauthenticated

## [0.2.7] — 2026-08-10

### Added

- Advertise `settings` capability so admin-ui discovers SettingsProvider without ListAll probing.

## [0.2.6] — 2026-08-10

### Added
- SettingsProvider mesh (`RegisterSettings`) for `prefer_workflow` routing toggle.


## [0.2.5] — 2026-08-10

### Fixed
- Sync Info()/muxcore.json version to **0.2.5**.

## v0.2.4 (2026-08-10)

- Auto-queue requested movie/TV items into media-automation (`AddToQueue`) asynchronously
- Soft-fail when automation is unavailable so request success is preserved
- Pin media-automation v0.1.5, media-movies v0.1.3, media-tvshows v0.1.4

## v0.2.3 (2026-08-09)

- Pin core/sdk to v0.5.1 for mTLS mesh dial

## v0.2.0 (2026-08-09)

- HTTP UI + JSON API for movie/TV requests
- Prefers workflow-tapestry; falls back to library Add / events
