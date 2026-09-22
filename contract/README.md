# kokoro-scheduler contract

Canonical machine source: [`openapi/v1/openapi.yaml`](./openapi/v1/openapi.yaml). The file uses the JSON representation accepted by YAML 1.2 and OpenAPI 3.1, so the repository can parse it with Go's standard JSON decoder without a second generated source. Human-readable policy in [`../docs/API_CONTRACT.md`](../docs/API_CONTRACT.md) is explanatory only.

## Owner and scope

- owner: `kokoro-scheduler`
- inbound visibility: `internal-owner`
- dispatch visibility: `event-protocol`
- version: `1.0.0`
- command prefix: `/internal/scheduler/v1/schedules/`

The contract covers the implemented health/readiness probes and tenant-scoped Schedule create/replace/delete/pause/resume commands. `components.schemas.Occurrence` records the owner-controlled occurrence lifecycle vocabulary. `webhooks.scheduleOccurrenceDispatch` defines the Scheduler-to-target POST/PUT body, stable occurrence identity headers, and retry classification. Occurrence and outbox persistence do not currently have query endpoints; the contract does not invent one.

It does not own BFF `ScheduledTask`, target-specific business commands, IAM authorization, or another repository's DTO/schema.

## Lint and runtime parity

Run the blocking contract gate:

```bash
./scripts/contract-check
```

The gate parses the canonical artifact, resolves every local `$ref`, verifies all operation governance extensions, checks Schedule/Occurrence/dispatch fields, exercises every declared control route against the Go handler, and compares dispatch headers with the concrete HTTP adapter. Empty documents and human-only descriptions do not pass.

## Generation

`contract/openapi/v1/openapi.yaml` is the canonical, owner-authored OpenAPI source; it is not generated from a second schema or code generator (`contract/manifest.json` records `generated: false`). Edit this artifact together with the implementation when the wire contract changes. The repository's authoritative generation/contract check is:

```bash
./scripts/contract-check
```

This command validates the canonical OpenAPI artifact and runs the contract/parity tests; no separate generator command is defined in this repository.

## Breaking policy

[`openapi/v1/breaking-policy.json`](./openapi/v1/breaking-policy.json) is the v1 compatibility signature. The gate fails when a protected path, method, Schedule input field, Occurrence field, dispatch header, retryable status, or stable control error mapping is removed. The canonical OpenAPI declares each operation's status-to-code mapping through `x-kokoro-control-error-codes`; the policy captures the required signature by `operationId`, and the contract test rejects any drift between the two. The protected owner codes are `schedule_already_exists` and `schedule_not_found`. Tightening constraints or changing semantics still requires owner review; an incompatible change must use a new major contract and coordinated consumer update rather than a runtime compatibility route.

Change order:

1. edit the canonical OpenAPI and implementation together;
2. update the compatibility signature only for an intentional reviewed contract decision;
3. recompute `contract/manifest.json`'s `source_sha256`;
4. run `./scripts/contract-check`, integration, and smoke gates;
5. publish consumers against an immutable commit and digest.

## Provenance

[`manifest.json`](./manifest.json) records owner, visibility, artifact path, semantic version, source mode, breaking-policy path, and SHA-256 digests for both the canonical artifact and compatibility signature. `TestContractProvenanceDigestMatchesCanonicalArtifact` blocks stale provenance. Recompute the digest with:

```bash
shasum -a 256 contract/openapi/v1/openapi.yaml
shasum -a 256 contract/openapi/v1/breaking-policy.json
```

A consumer provenance record is `{repository, commit, artifact, contract_version, source_sha256}`; a mutable branch or copied file alone is insufficient.
