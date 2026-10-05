# Changelog

## [0.3.5] - 2026-10-05


### Changed
- Reported version comes from muxcore.json (ADR-0021); built on core v0.6.12 / sdk/go/module v0.6.3 (mesh enrollment, ADR-0017).

## [0.3.4] - 2026-10-05


### Security
- ADR-0019 / NFR-SEC-007: end-user identity now comes from `Authorization: Bearer <token>` resolved via the identity provider's `ExtractIdentity` (discovered through core by capability `identity`, or pinned with `AUTH_LOCAL_GRPC_ADDR`; 30 s cache keyed by sha256(token)). Unknown token -> 401, provider unreachable -> 503. `X-Caller-Id`/`X-MuxCore-User` alone are no longer trusted; an `X-Caller-Id` that differs from the token user -> 403.
- Legacy header-only identity requires both `MUXCORE_INSECURE_DISABLE_TLS=true` and `REQUEST_TRUST_CALLER_HEADER=1` (warns once).
- gRPC: unary interceptor resolves `authorization` metadata the same way (gRPC handlers previously never had a caller).
- Allowlisted mesh modules (verified mTLS client-cert CN in `REQUEST_MODULE_PRINCIPALS`, default `media-library-maintainer`) may call only list/deny without a user token.
- Authorizer and identity-provider dials use mesh TLS (`MUXCORE_TLS_CERT/KEY/CA`); plaintext only with `MUXCORE_INSECURE_DISABLE_TLS=true`.

## [0.3.3] - 2026-10-05


### Added
- Upgrade test (`internal/reqstore/upgrade_test.go`) with snapshot fixtures from v0.2.7, v0.3.0 (commit e89fabf, untagged) and v0.3.2 under `internal/reqstore/testdata/upgrade/` (ADR-0015, NFR-DATA-002, FR-INS-005). No migration bugs found.

### Changed
- `core/sdk/go/module` v0.6.0 -> v0.6.1 (test helper `moduletest`).

## [0.3.2] - 2026-10-05

### Fixed
- End-user caller identity uses a module-local context key (`authz.WithCallerID` / `authz.CallerID`) instead of the removed core helpers; empty caller still fails closed.
- `make proto` writes generated code under `proto/`.

### Changed
- Dependencies resolve from published GitHub tags (core v0.6.2, media-automation v0.1.46); CI from the umbrella template.

## [0.3.3] — 2026-09-08

### Added

- Seerr-style household quotas: `REQUEST_MAX_PENDING_PER_USER`, `REQUEST_MAX_PER_WEEK`, `REQUEST_AUTO_APPROVE_USERS`
- Settings + persisted `request-policy.json` + HTTP `GET|PUT /api/request-policy`
- HTTP caller also accepts `X-MuxCore-User` (media-ui BFF)

## [0.3.2] — 2026-09-06

### Added

- Publish `media.request.ready` once when a user-requested title becomes playable
  (`has_file` / file-added / imported — not `status=available`)
- Subscribe to `media.movie.file.added`, `media.tv.episode.file.added`,
  `media.file.imported` (plus wiki-era aliases) after mesh connect
- Dedupe table `request_ready_notified` keyed by `request_id`

## [0.3.1] — 2026-09-05

### Added

- Inbound gRPC TLS by default (`grpc.Creds`) with auto-generated mesh-local certs when `REQUEST_TLS_*` / `MUXCORE_TLS_*` are unset
- Default gRPC bind `127.0.0.1:9481` (override via `REQUEST_GRPC_ADDR`)

### Changed

- Plaintext gRPC requires explicit `MUXCORE_INSECURE_DISABLE_TLS=true` or `MUXCORE_GRPC_INSECURE=true` (dev escape hatch)

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
