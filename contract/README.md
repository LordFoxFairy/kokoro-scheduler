# kokoro-scheduler Contract

Canonical machine source: [`openapi/v1/openapi.yaml`](./openapi/v1/openapi.yaml). Human-readable runtime semantics live in
[`../docs/API_CONTRACT.md`](../docs/API_CONTRACT.md); they do not form a second wire source.

## Owner

**Owner:** `kokoro-scheduler`.

The contract covers only generic Schedule registration/control, health/readiness, and Scheduler-to-owner dispatch metadata.
It does not define BFF `ScheduledTask`, target business commands, tenant authorization, business receipt, or another repository's
DTO/Schema.

## Visibility

**Visibility:** `internal-owner`.

The listener is for trusted service/deployment traffic. It is not a public Product API, browser API, OAuth resource, AG-UI stream,
or Developer API portal source. `/healthz` and `/readyz` are unauthenticated operational probes but remain part of this internal
service contract.

## Version

**Version:** `1.0.0`, with versioned command paths under `/internal/scheduler/v1/`.

The OpenAPI `info.version` is authoritative for the document version. Probe paths are intentionally unversioned operational
endpoints; command resources carry the explicit `v1` path segment.

## Generation

**Generation:** the OpenAPI YAML is owner-authored and code-reviewed in this repository; it is not generated from Go structs.
The current repository has no generated client/server tree. Runtime transport tests validate the implemented boundary, and the
architecture test checks that the canonical document contains every HTTP route.

If generated clients are introduced, they must be derived from this exact versioned source, written only to an explicit generated
directory, carry source version/commit/digest metadata, and never become Domain models or an editable contract copy.

## Breaking changes

**Breaking policy:** v1 changes must remain backward compatible. Removing/renaming a path, method, header, request/response field,
status/error code, tightening an accepted constraint, changing authentication/idempotency semantics, or changing an extension's
meaning requires a new major contract path/version and coordinated consumer migration. Optional additive fields or responses still
require owner review, tests, docs, and an `info.version` update appropriate to semantic impact.

Change order:

1. update this owner source;
2. run OpenAPI governance and transport contract checks;
3. update implementation/tests and human docs in the same commit;
4. publish/pin the contract artifact by immutable Git revision and digest;
5. update consumers from that pinned provenance;
6. run integration/smoke before rollout.

There is currently no standalone historical OpenAPI breaking-diff tool in this Go repository. Breaking safety therefore depends on
review plus version discipline until such a gate is added; a governance-key check is not a substitute for compatibility analysis.

## Provenance

**Provenance:** `https://github.com/LordFoxFairy/kokoro-scheduler`, path
`contract/openapi/v1/openapi.yaml`. The content digest for this revision is:

```text
sha256:480cbc538d6cdb58c36212132b8e012e6d8161969bcfdfe49041ea12ac1eb9c0
```

Consumers must record `{repository, git tag-or-commit, path, info.version, sha256}`. Recompute and verify with:

```bash
shasum -a 256 contract/openapi/v1/openapi.yaml
git rev-parse HEAD
```

A mutable branch name or copied YAML without its commit and digest is not valid provenance.

## Operation governance extensions

Every operation carries all five fields:

| Extension | v1 values | Meaning |
|---|---|---|
| `x-kokoro-owner` | `kokoro-scheduler` | This repository owns the operation |
| `x-kokoro-visibility` | `internal-owner` | Trusted internal service surface only |
| `x-kokoro-stability` | `stable` | v1 compatibility policy applies |
| `x-kokoro-idempotency` | `inherent` / `required` | GET probes are inherent; every mutation requires `Idempotency-Key` |
| `x-kokoro-permission` | `none` / `service-token` | probes are open; commands require the configured shared Bearer token |

`service-token` describes current enforcement and does not assert an IAM fine-grained permission that the runtime does not check.

## Checks

```bash
go test ./internal/architecture ./internal/transport/http
```

The Kokoro Root workspace also provides the targeted governance invocation documented in
[`../docs/ACCEPTANCE.md`](../docs/ACCEPTANCE.md), which checks route versioning, snake_case wire properties, all five operation
extensions, and the required README governance fields.
