# Compatibility

## Core Version

| Module Version | Core Version | Status |
|----------------|-------------|--------|
| v0.3.1           | 0.4.0+     | Current |
| v0.3.0           | 0.4.0+     | Previous |

MVP host stacks pin **core@v0.5.0**. This module declares `minCoreVersion` **0.4.0**.

## Capabilities

- `media.request`

## Contracts

No external contract package — `RequestService` gRPC + HTTP JSON UI/API.

### Request statuses

| Status | Meaning |
|--------|---------|
| `pending` | Awaiting household approval |
| `denied` | Rejected; see `deny_reason` on `GetStatus` / `ListRequests` |
| `watchlisted` | Saved for later; no acquisition handoff |
| `requested` | Approved; queued when library module unavailable |
| `added` | Approved; added to library |
| `workflow` | Approved; handed to workflow engine |

After an identity-provider user erasure (ADR-0035) `requested_by` of the user's `requested` / `added` / `workflow`
requests reads `deleted-user`, the user's `pending` / `denied` / `watchlisted` requests no longer exist, and that id is
refused for create / watchlist / approve / deny with HTTP 403 `{"code": "request.user_erased"}` (gRPC `PermissionDenied`
with the same prefix).

### Authorization

Uses mesh capability **`authorizer`** (`AuthService.Can`) with resource **`media.request`**:

| Action | Used for |
|--------|----------|
| `create` | Submit requests |
| `list` | List / filter requests |
| `approve` | Approve pending requests (also enables auto-approve on create) |
| `deny` | Deny pending requests |
| `watchlist` | Add/remove watchlist entries |

Set `REQUEST_REQUIRE_APPROVAL=false` to restore pre-v0.3.0 immediate handoff behavior.

AI tickets (`ai-tickets`) resolve wrong-language reports into a `replace_media` action. This module stays non-AI: consume that action by creating a normal request through the existing HTTP/gRPC APIs (or `media.movie.requested`).

## Breaking Changes

**v0.3.0** — New requests default to **`pending`** when `REQUEST_REQUIRE_APPROVAL=true` (default). Approve/deny RPCs and extended `GetStatus` fields (`deny_reason`, `requested_by`) added. Pre-1.0 module: interfaces may still change without a major version bump.
