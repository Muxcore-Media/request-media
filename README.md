# Request Media

Web UI and gRPC API for requesting movies and TV shows into the MuxCore media pipeline.

## Key Features

- HTTP UI (`/`) with TMDB movie search, request form, and recent-request history
- HTTP JSON API: `GET /api/search`, `POST /api/request`, `GET /api/requests`, approve/deny actions, watchlist
- gRPC `RequestService`: `RequestMovie`, `RequestTV`, `GetStatus`, `ListRequests`, `ApproveRequest`, `DenyRequest`, `AddToWatchlist`, `RemoveFromWatchlist`
- Household approval loop: new requests start as `pending` until an approver calls `ApproveRequest` (or auto-approve when the caller has approve permission)
- Prefers workflow engine (`movie-request` / `tv-request`); falls back to `media.library` `AddMovie` for movies, or event-only `requested` status after approval
- Publishes `media.movie.requested` / `media.tv.requested` events via the core mesh

## Approval loop

1. A household member submits a request (`RequestMovie` / `RequestTV` or `POST /api/request`).
2. When `REQUEST_REQUIRE_APPROVAL=true` (default), the record is stored as **`pending`** and a `media.request.pending` event is published.
3. An approver with `approve` permission on resource `media.request` calls **`ApproveRequest`**, which runs the existing library / workflow / automation handoff.
4. To reject, call **`DenyRequest`** with a reason (visible on `GetStatus` / `ListRequests`).
5. Callers with approve permission are **auto-approved** on create (trusted role shortcut).

Permission checks use the mesh **`authorizer`** module (`AuthService.Can`) and caller identity from `x-caller-id` / `X-Caller-Id` metadata.

### HTTP identity and authz hardening

- **`GET /api/requests`** and **`GetStatus`** require the same `list` permission on resource `media.request` as gRPC `ListRequests`.
- When the authorizer module cannot be resolved, permission checks **fail closed** (deny) instead of allowing the action. This applies to create, approve, deny, list, and watchlist paths.
- **`X-Caller-Id` is spoofable** on direct HTTP access without the MuxCore mesh proxy. Do not expose the HTTP port to untrusted networks without the proxy in front. Missing `X-Caller-Id` leaves the caller unauthenticated and authz checks deny the request.
- Hard mesh verification of caller identity (beyond header trust) is planned follow-up work; until then, run behind the mesh proxy and keep the authorizer module available.

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `REQUEST_GRPC_ADDR` | `:9481` | gRPC listen address |
| `REQUEST_HTTP_ADDR` | `:9380` | HTTP UI / JSON API listen address |
| `REQUEST_REQUIRE_APPROVAL` | `true` | Hold new requests in `pending` until approved |
| `REQUEST_PREFER_WORKFLOW` | `true` | Prefer workflow engine before library add |
| `REQUEST_DATA_DIR` | `data` | SQLite persistence directory |
| `MUXCORE_GRPC_ADDR` | `localhost:9090` | Core mesh gRPC address (client dial) |
| `MUXCORE_INSECURE_DISABLE_TLS` | unset | Set `true` to disable TLS for module SDK / mesh dial |

## HTTP JSON (approval)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/requests?status=pending` | List requests (requires `list` permission; optional status filter) |
| `POST` | `/api/requests/{id}/approve` | Approve a pending request |
| `POST` | `/api/requests/{id}/deny` | Deny with JSON body `{ "reason": "..." }` |
| `POST` | `/api/watchlist` | Save for later (`watchlisted` status) |
| `DELETE` | `/api/watchlist/{id}` | Remove watchlist entry |

Pass caller identity via header `X-Caller-Id` when testing HTTP without the mesh proxy. The header is **not authenticated** unless the mesh proxy sets it; omit it to exercise fail-closed authz behavior.

## Capability

`media.request` — media request intake (role `media_request`)

## Dependencies

- `github.com/Muxcore-Media/core` — contracts, workflow proto, module SDK, mesh client, auth authorizer
- `github.com/Muxcore-Media/media-movies` — `media.library` movie add (fallback path)
- `github.com/Muxcore-Media/metadata-tmdb` — TMDB search for the HTTP UI
