# Upgrade snapshots (ADR-0015, T-M2-06)

Each `<tag>.db` was produced by that version's own `reqstore.Open` + `Put` (+ `ClaimReadyNotified` where it exists),
then `sqlite3 <db> VACUUM` (or `python3 -c "import sqlite3; sqlite3.connect('<db>').execute('VACUUM')"`). `<tag>.schema.sql` is `sqlite3 <db> .schema`.

| Snapshot | Source | Tables seeded |
|----------|--------|---------------|
| `v0.2.7.db` | tag `v0.2.7` (schema identical to v0.2.0..v0.2.7; the previous tag before the latest tag `v0.3.2`) | `requests` (3 rows) |
| `v0.3.0.db` | commit `e89fabf` ("household request approval queue (v0.3.0)"); **no `v0.3.0` git tag exists** | `requests` (3 rows, with requested_by / deny_reason / season / episode / overview / genres_json) |
| `v0.3.2.db` | tag `v0.3.2` (reqstore identical to v0.3.7) | `requests` (3 rows, + quality_profile_id), `request_ready_notified` (2 rows) |
| `v0.3.7.db` | tag `v0.3.7` = `10483aa` (the current release at ADR-0035 slice E4; schema identical to v0.3.2, **no** `erasure_applied`); seeded with the `v0.3.2` seed file, which compiles unchanged at this tag | `requests` (3 rows, + quality_profile_id), `request_ready_notified` (2 rows) |

Seed rows: req-1 (movie, available, alice@example.com), req-2 (movie, pending, bob@example.com),
req-3 (episode S2E5, denied "already owned elsewhere", carol@example.com). `updated_at` is pinned to `created_at + 1m`.

Reproduce: `git worktree add /tmp/rm-<v> <tag-or-commit>`, copy the matching `seed_upgrade_test.<v>.go.txt` to
`internal/reqstore/seed_upgrade_test.go` (build tag `upgradeseed`) and run
`UPGRADE_SEED_DB=/tmp/seed.db GOWORK=off go test -tags upgradeseed -run TestUpgradeSeed ./internal/reqstore/`, then
`sqlite3 /tmp/seed.db VACUUM`. The `.go.txt` files do not compile in the current tree.
