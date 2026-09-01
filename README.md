# Request Media

Web UI and gRPC API for requesting movies, TV, music, books, comics, and audiobooks into the MuxCore media pipeline.

## Key Features

- HTTP UI (`/`) with TMDB movie/TV search, request form, and recent-request history
- HTTP JSON API:
  - `GET /api/search` — TMDB movie/TV search or MusicBrainz artist/album/track search (`type=music|music_album|music_track`)
  - `GET /api/discover/{movie|tv}/{tmdbId}` — detail pages with trailer metadata
  - `GET /api/discover/{trending|popular}/{movie|tv}` — browse lists
  - `POST /api/request` — create a request (`mediaType`: `movie`, `tv`, `music`, `music_album`, `music_track`, `book`, `comic`, `audiobook`)
  - `GET /api/requests` — list requests (optional `?status=pending`)
  - `POST /api/requests/{id}/{approve|deny|cancel}` — approval workflow
- gRPC `RequestService`: `RequestMovie`, `RequestTV`, `RequestMusic`, `RequestAlbum`, `RequestTrack`, `RequestBook`, `RequestComic`, `RequestAudiobook`, `GetStatus`, `ListRequests`, `ApproveRequest`, `DenyRequest`, `CancelRequest`
- Search/discover overlay `requestStatus` when a title is already requested or in-library
- Prefers workflow engine (`movie-request` / `tv-request`); falls back to library `Add*` RPCs
- Publishes `media.movie.requested` / `media.tv.requested` / music requested events via the core mesh

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `REQUEST_GRPC_ADDR` | `:9481` | gRPC listen address |
| `REQUEST_HTTP_ADDR` | `:9380` | HTTP UI / JSON API listen address |
| `REQUEST_DATA_DIR` | `data` | SQLite request store directory |
| `REQUEST_REQUIRE_APPROVAL` | `false` | When `true`, non-privileged users stay `pending` until approve |
| `REQUEST_MANAGER_AUTO_APPROVE` | `true` | Managers skip approval when `REQUEST_REQUIRE_APPROVAL=false` |
| `REQUEST_ALLOWED_ROLES` | `admin,manager,user` | Comma-separated roles allowed to submit requests |
| `REQUEST_ALLOW_ANONYMOUS` | unset | Set `true` to allow requests without `X-MuxCore-Roles` |
| `REQUEST_PREFER_WORKFLOW` | `true` | Prefer workflow engine over direct library add |
| `MUXCORE_GRPC_ADDR` | `localhost:9090` | Core mesh gRPC address (client dial) |
| `MUXCORE_INSECURE_DISABLE_TLS` | unset | Set `true` to disable TLS for module mesh dials (dev only) |
| `MUXCORE_TLS_CERT` / `MUXCORE_TLS_KEY` / `MUXCORE_TLS_CA` | unset | Client TLS for mesh dials when insecure mode is off |

gRPC callers should pass roles via metadata `x-muxcore-roles` (comma-separated) and user via `x-muxcore-user`. HTTP uses `X-MuxCore-Roles` / `X-MuxCore-User` headers.

## Capability

`media.request` — media request intake (role `media_request`)

## Dependencies

- `github.com/Muxcore-Media/core` — contracts, workflow proto, module SDK, mesh client
- `github.com/Muxcore-Media/media-movies` / `media-tvshows` / `media-music` — library add paths
- `github.com/Muxcore-Media/media-books` / `media-comics` / `media-audiobooks` — household library intake
- `github.com/Muxcore-Media/metadata-tmdb` / `metadata-musicbrainz` — search metadata
- `github.com/Muxcore-Media/media-automation` — acquisition queue after approve/add
