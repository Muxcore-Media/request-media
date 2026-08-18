# Changelog

## [0.2.11] — 2026-08-18

### Fixed
- Requests with a matching missing wanted row and no in-flight grab show `searching` instead of staying `added`.

## [0.2.10] — 2026-08-18

### Fixed
- TV requests with season 0 and a dummy episode (`S00E12`) enqueue a series pack (`episode == 0`) instead of a placeholder wanted row.

## [0.2.9] — 2026-08-18

### Fixed
- Treat `stalled` and `import_failed` automation history as `downloading` so requests with an in-flight or stuck grab leave `added`.

## [0.2.8] — 2026-08-18

### Added
- Advance request status from `added`/`requested` using media-automation queue + history (`downloading` when a grab is in flight or the library is partial; `available` when no real episodes/movies remain missing). Season-0 dummy wanted rows do not block `available`. Refresh on list/GetStatus and every 60s.

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
