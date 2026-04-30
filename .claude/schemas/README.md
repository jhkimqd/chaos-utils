# JSON Schemas (advisory)

These schemas describe the shape of the chaos-utils scenario YAML and the
test-report JSON. They are **advisory** — the Go validator and encoder
remain canonical:

| Schema | Derived from | Canonical Go source |
| ------ | ------------ | ------------------- |
| `scenario.schema.json` | scenario YAML | `pkg/scenario/types.go` + `pkg/scenario/validator/validator.go` |
| `report.schema.json` | test-report JSON written under `reports/` | `pkg/reporting/types.go` (+ `pkg/core/cleanup` for cleanup blocks) |

The Go YAML decoder silently ignores unknown keys today — that is a
foot-gun, since a typo in `success_critera:` produces zero criteria and
no error. These schemas opt in to stricter checking by setting
`additionalProperties: false` on the top-level `Scenario`, on `Spec`, and
on most nested objects, so a schema validator catches typos that the
runner would not.

When the schema and the Go source disagree, **the Go source wins**.
Update the schema in the same change that updates the source.

## Validating a scenario locally

Preferred (`yq` + `ajv-cli` already installed):

```sh
npx -y ajv-cli@5 validate \
  --spec=draft2020 \
  --all-errors \
  -c ajv-formats \
  -s .claude/schemas/scenario.schema.json \
  -d <(yq -o json scenarios/polygon-chain/network/single-node-isolation.yaml)
```

Fallback installs:

```sh
go install github.com/mikefarah/yq/v4@latest
npm install -g ajv-cli ajv-formats
```

Validate every built-in scenario:

```sh
for f in $(find scenarios -name '*.yaml'); do
  echo "== $f =="
  npx -y ajv-cli@5 validate --spec=draft2020 -c ajv-formats \
    -s .claude/schemas/scenario.schema.json \
    -d <(yq -o json "$f") || echo "FAIL: $f"
done
```

## Validating a report

```sh
npx -y ajv-cli@5 validate --spec=draft2020 -c ajv-formats \
  -s .claude/schemas/report.schema.json \
  -d reports/test-*.json
```

## What these schemas cover (and don't)

Covered:

- `apiVersion` / `kind` enums and the `metadata.name` regex.
- `spec.duration` / `warmup` / `cooldown` as Go duration strings.
- `targets[]` selector type-specific required fields (`kurtosis_service`
  needs `pattern` or `service_name`; `docker_container` needs `pattern`,
  `container_id`, or `labels`).
- The full registered fault-type enum (in sync with
  `validator.go::validateFaultType`'s `validTypes`).
- `network` fault `params` shape and bounds (`packet_loss` 0-100,
  `latency` ≥ 0, `bandwidth` ≥ 0, `target_proto` enum).
- `success_criteria[]` per-type required fields:
  - `prometheus` → `query`, `threshold`
  - `log` → `pattern`
  - `command` → `exec[0]` non-empty
  - `state_root_consensus` → no extra required fields.
- Report top-level required fields and the per-record shapes for
  `targets[]`, `faults[]`, `success_criteria[]`, and the cleanup blocks.

Not covered:

- Cross-field invariants the Go validator enforces — e.g. each
  `fault.target` must reference a real `targets[].alias`, and target
  aliases must be unique. JSON Schema cannot express
  uniqueness-by-sub-property cleanly; trust the Go validator for those.
- Per-fault-type `params` shapes other than `network`. The Go parser
  silently drops unknown keys, so the README's "Fault parameters"
  section is the user-facing source of truth there.
- Semantic-corruption rule files under
  `scenarios/polygon-chain/semantic/rules/`. See
  `_REFERENCE.yaml` and `pkg/injection/http/corruption/rules.go`.
