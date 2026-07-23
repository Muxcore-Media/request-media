# Request Media

Web UI and gRPC API for requesting movies and TV shows into the MuxCore media pipeline.

## Key Features

- HTTP UI (`/`) with TMDB movie search, request form, and recent-request history
- HTTP JSON API: `GET /api/search`, `POST /api/request`, `GET /api/requests`
- gRPC `RequestService`: `RequestMovie`, `RequestTV`, `GetStatus`
- Prefers workflow engine (`movie-request` / `tv-request`); falls back to `media.library` `AddMovie` for movies, or event-only `requested` status
- Publishes `media.movie.requested` / `media.tv.requested` events via the core mesh

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `REQUEST_GRPC_ADDR` | `:9480` | gRPC listen address |
| `REQUEST_HTTP_ADDR` | `:9380` | HTTP UI / JSON API listen address |
| `MUXCORE_GRPC_ADDR` | `localhost:9090` | Core mesh gRPC address (client dial) |
| `MUXCORE_INSECURE_DISABLE_TLS` | unset | Set `true` to disable TLS for module SDK / mesh dial |

## Capability

`media.request` — media request intake (role `media_request`)

## Dependencies

- `github.com/Muxcore-Media/core` — contracts, workflow proto, module SDK, mesh client
- `github.com/Muxcore-Media/media-movies` — `media.library` movie add (fallback path)
- `github.com/Muxcore-Media/metadata-tmdb` — TMDB search for the HTTP UI
