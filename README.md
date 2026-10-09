# Request Media

Web UI and gRPC API for requesting movies and TV shows into the MuxCore media pipeline.

## Key Features

- HTTP UI (`/`) with TMDB movie search, request form, and recent-request history
- HTTP JSON API: `GET /api/search`, `POST /api/request`, `GET /api/requests`, approve/deny actions, watchlist
- gRPC `RequestService`: `RequestMovie`, `RequestTV`, `GetStatus`, `ListRequests`, `ApproveRequest`, `DenyRequest`, `AddToWatchlist`, `RemoveFromWatchlist`
- Household approval loop: new requests start as `pending` until an approver calls `ApproveRequest` (or auto-approve when the caller has approve permission)
- Prefers workflow engine (`movie-request` / `tv-request`); falls back to `media.library` `AddMovie` for movies, or event-only `requested` status after approval
- Publishes `media.movie.requested` / `media.tv.requested` events via the core mesh
- Publishes `media.request.ready` once when a requested title becomes **playable**

## Playable-ready event (`media.request.ready`)

This module is the **emitter** for Discord/webhook "your request is ready" notifications
(umbrella#83). playback-monitor consumes the event and dispatches matching rules.

**Playable means `has_file`**, the same semantics as media-ui ready toasts — **not**
`status=available` and **not** request status `added` (cataloged). After mesh connect,
request-media subscribes to:

- `media.movie.file.added` / legacy `media.movie.file_added`
- `media.tv.episode.file.added` / legacy `media.tv.episode_file_added`
- `media.file.imported`

Matching watchable requests (`requested` / `added` / `workflow`; not `pending`,
`denied`, or `watchlisted`) are selected by `item_id` / `movie_id` / `series_id`
or `tmdb_id` + `item_type`. The event is published **once per `request_id`**
(`request_ready_notified` table). Payload:

| Field | JSON |
|-------|------|
| Request ID | `request_id` |
| Requester | `requested_by` |
| Title | `title` |
| Year | `year` |
| Item type | `item_type` |
| TMDB ID | `tmdb_id` |
| Library item | `item_id` |

Unconfigured webhook destinations remain a quiet no-op in playback-monitor.

## Approval loop

1. A household member submits a request (`RequestMovie` / `RequestTV` or `POST /api/request`).
2. When `REQUEST_REQUIRE_APPROVAL=true` (default), the record is stored as **`pending`** and a `media.request.pending` event is published.
3. An approver with `approve` permission on resource `media.request` calls **`ApproveRequest`**, which runs the existing library / workflow / automation handoff.
4. To reject, call **`DenyRequest`** with a reason (visible on `GetStatus` / `ListRequests`).
5. Callers with approve permission, or user ids listed in `REQUEST_AUTO_APPROVE_USERS`, are **auto-approved** on create.
6. Optional household quotas (`REQUEST_MAX_PENDING_PER_USER`, `REQUEST_MAX_PER_WEEK`) return HTTP 429 `{ "code": "request.quota" }` when exceeded.

Permission checks use the mesh **`authorizer`** module (`AuthService.Can`) and caller identity from `x-caller-id` / `X-Caller-Id` metadata.

### HTTP identity and authz hardening

- **`GET /api/requests`** and **`GetStatus`** require the same `list` permission on resource `media.request` as gRPC `ListRequests`.
- When the authorizer module cannot be resolved, permission checks **fail closed** (deny) instead of allowing the action. This applies to create, approve, deny, list, and watchlist paths.
- **`X-Caller-Id` is spoofable** on direct HTTP access without the MuxCore mesh proxy. Do not expose the HTTP port to untrusted networks without the proxy in front. Missing `X-Caller-Id` leaves the caller unauthenticated and authz checks deny the request.
- Hard mesh verification of caller identity (beyond header trust) is planned follow-up work; until then, run behind the mesh proxy and keep the authorizer module available.

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `REQUEST_GRPC_ADDR` | `127.0.0.1:9481` | gRPC listen address (loopback by default; override for Docker/all-interfaces) |
| `REQUEST_HTTP_ADDR` | `:9380` | HTTP UI / JSON API listen address |
| `REQUEST_REQUIRE_APPROVAL` | `true` | Hold new requests in `pending` until approved |
| `REQUEST_PREFER_WORKFLOW` | `true` | Prefer workflow engine before library add |
| `REQUEST_MAX_PENDING_PER_USER` | `0` | Max pending requests per user (`0` unlimited) |
| `REQUEST_MAX_PER_WEEK` | `0` | Rolling 7-day request cap per user (`0` unlimited) |
| `REQUEST_AUTO_APPROVE_USERS` | unset | Comma-separated user ids that skip the pending queue |
| `REQUEST_DATA_DIR` | `data` | SQLite persistence directory |
| `ERASURE_SWEEP_INTERVAL` | `5m` | How often the user-erasure reconciler reads the identity provider's ledger (Go duration, clamped to 30s..24h, jittered +/-20%). An unparsable value fails startup |
| `MUXCORE_GRPC_ADDR` | `localhost:9090` | Core mesh gRPC address (client dial) |
| `MUXCORE_INSECURE_DISABLE_TLS` | unset | Set `true` to disable TLS for inbound gRPC and module SDK / mesh dial |
| `MUXCORE_GRPC_INSECURE` | unset | Alias for `MUXCORE_INSECURE_DISABLE_TLS` |
| `REQUEST_TLS_CERT` / `REQUEST_TLS_KEY` / `REQUEST_TLS_CA` | unset | Optional PEM paths for inbound gRPC TLS (falls back to `MUXCORE_TLS_*`, then auto-generated certs under `REQUEST_TLS_DIR` or `~/.muxcore/tls/request-media`) |

## User erasure (ADR-0035, NFR-DATA-003)

When the identity provider deletes a user it records an erasure tombstone in its ledger. request-media runs the
SDK's `erasure.Reconciler` (`sdk/go/module/erasure`): at startup and every `ERASURE_SWEEP_INTERVAL` it reads the whole ledger from the provider of the exclusive `identity` capability (certificate CN must
equal the provider's module id), applies each tombstone it has not applied, checks the post-condition and
acknowledges. **The ledger is the only trigger**: no event, header or HTTP request erases anything. Requires the
module's mesh certificate (CN = module id) and listing in the provider's `AUTH_ERASURE_CONSUMERS` /
`AUTH_ERASURE_REQUIRED`. The household profile fails startup if the reconciler cannot run; plaintext (dev) follows
`meshtls` rules.

| Data | Disposition |
|------|-------------|
| `requests` in `watchlisted`, `pending`, `denied` | deleted, with their `request_ready_notified` rows |
| `requests` in any other state (`requested`, `added`, `workflow`, legacy `available`) | household acquisition record kept; `requested_by` becomes `deleted-user` |
| `request-policy.json` `autoApproveUsers` | the id is removed (atomic rewrite, file mode and other users preserved) |
| quotas | computed from `requests`, nothing stored; votes and comments do not exist |

Two phases, because the policy file is outside SQLite: (1) one SQLite transaction deletes/anonymises the rows and
inserts the `erasure_applied` row with `policy_done = 0`; (2) the id is removed from the policy file and
`policy_done` is set to 1. The tombstone counts as applied (and is acknowledged OK) only after phase 2, so a crash
between the phases is finished by the next sweep. Once the `erasure_applied` row exists the erased id is refused
(`403 {"code": "request.user_erased"}`, gRPC `PermissionDenied`) for create, watchlist, approve and deny, and a token
that still resolves for it is treated as invalid. `REQUEST_AUTO_APPROVE_USERS` is operator configuration the module
cannot rewrite: an erased id is dropped from it at runtime and on every start, but the variable should be edited.
Archives made before the deletion are not rewritten (ADR-0035 §4).

## HTTP JSON (approval)

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/requests?status=pending` | List requests (requires `list` permission; optional status filter) |
| `POST` | `/api/requests/{id}/approve` | Approve a pending request |
| `POST` | `/api/requests/{id}/deny` | Deny with JSON body `{ "reason": "..." }` |
| `GET` | `/api/request-policy` | Current limits + remaining quota for the caller |
| `PUT` | `/api/request-policy` | Update limits (requires `approve` permission) |
| `POST` | `/api/watchlist` | Save for later (`watchlisted` status) |
| `DELETE` | `/api/watchlist/{id}` | Remove watchlist entry |

Pass caller identity via header `X-Caller-Id` when testing HTTP without the mesh proxy. The header is **not authenticated** unless the mesh proxy sets it; omit it to exercise fail-closed authz behavior.

## Capability

`media.request` — media request intake (role `media_request`)

## Dependencies

- `github.com/Muxcore-Media/core` — contracts, workflow proto, module SDK, mesh client, auth authorizer
- `github.com/Muxcore-Media/media-movies` — `media.library` movie add (fallback path)
- `github.com/Muxcore-Media/metadata-tmdb` — TMDB search for the HTTP UI
