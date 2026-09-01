# Compatibility

## Core Version

| Module Version | Core Version | Status |
|----------------|-------------|--------|
| v0.3.0           | 0.4.0+     | Current |

MVP host stacks pin **core@v0.5.x**. This module declares `minCoreVersion` **0.4.0**.

## Capabilities

- `media.request`

## Contracts

Local `RequestService` gRPC (`proto/requestmedia/requestmedia.proto`) plus HTTP JSON routes documented in `README.md`.

## Breaking Changes

Pre-1.0 module: interfaces may change without a major version bump. Prefer published tags over `main`/`master` HEAD in production.
