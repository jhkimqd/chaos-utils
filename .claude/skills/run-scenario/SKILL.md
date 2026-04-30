---
name: run-scenario
description: Orchestrate an end-to-end chaos run for chaos-utils — build, dry-run validate, execute, locate the JSON report, and hand off to triage. Trigger on phrases like "run a chaos scenario", "execute scenarios/...yaml", "kick off the chaos test for X", "run the validator partition scenario", or "do an end-to-end chaos run".
---

# run-scenario — chaos-runner orchestration

Drive one chaos experiment from source tree to triaged report. Follow
the steps **in order**. Each step has a hard stop condition — honor it.

The scenario path is required. If the user described it by intent
("the validator partition test"), grep `scenarios/` first and confirm
the file before proceeding.

---

## Step 1 — Build

Ensure `./bin/chaos-runner` exists and is fresher than every Go source
file under `cmd/` and `pkg/`:

```bash
if [ ! -x ./bin/chaos-runner ] || [ -n "$(find cmd pkg -name '*.go' -newer ./bin/chaos-runner -print -quit 2>/dev/null)" ]; then
    make build-runner
fi
```

`make` (default target) builds all three binaries; `make build-runner`
builds only the host CLI. If the build fails, **stop**.

---

## Step 2 — Dry-run validate

```bash
./bin/chaos-runner run --dry-run --scenario <path>
```

Non-zero exit ⇒ surface the validator output and **stop**. Most dry-run
failures are scenario YAML bugs (unknown fault type, missing required
field, malformed PromQL).

**Exit-code contract** (cmd/chaos-runner/main.go:55-66):

| Code | Meaning |
| ---- | ------- |
| `0`  | OK — validated / executed and met all critical criteria. |
| `1`  | Validation or test-criteria failure. |
| `2`  | Infrastructure error (`InfraError`) — the framework couldn't start. |

CI keys off this: code 1 = real test finding, code 2 = pipeline broken.

---

## Step 3 — Confirm preconditions

Walk this checklist out loud so the user can interrupt. Stop on any failure.

1. **Kurtosis enclave is up.** Look up `kurtosis.enclave_name` in
   `config.yaml` (default `pos-network` per README Quick Start), then:
   ```bash
   kurtosis enclave inspect <enclave-name>
   ```
2. **Pre-fault Prometheus health is green.** No critical alerts on the
   devnet. The orchestrator's pre-check phase re-verifies, but a manual
   look saves a wasted run.
3. **No prior chaos test still active.**
   - `ls -lt reports/test-*.json | head -3` — is the newest report's
     `status` still `running`?
   - `docker ps --filter name=chaos-` — should be empty between runs.
     If residue exists, see "Recovery" before starting.
4. **Enclave matches the scenario's expected validator topology.**
   Built-in scenarios assume the 8-validator Polygon devnet and target
   validator 4 (CLAUDE.md §5.2). On a different shape, check the
   scenario doesn't hardcode validator indices that won't exist.

---

## Step 4 — Execute

```bash
./bin/chaos-runner run --scenario <path> [--set key=value ...] [--enclave <name>] [--format text|json|tui] [--config <path>] [-v]
```

Flag reference (cmd/chaos-runner/run.go:38-44):

| Flag                  | Notes |
| --------------------- | ----- |
| `--scenario` (string) | **Required.** Path to scenario YAML. |
| `--set` (repeatable)  | Override scenario values. Format `key=value`. |
| `--enclave` (string)  | Override `config.yaml`'s `kurtosis.enclave_name`. |
| `--format` (string)   | `text` (default), `json`, or `tui`. |
| `--dry-run` (bool)    | Used in Step 2. |
| `--config` (string)   | Path to `config.yaml` (root flag, default `./config.yaml`). |
| `-v` / `--verbose`    | Debug-level logging (root flag). |

**Common `--set` overrides.** The scenario YAML decides which keys it
templates (the parser silently ignores unknown keys, so spell-check).
Universal: `duration` (e.g. `10m`), `warmup` (e.g. `30s`). Fault-specific
keys appear in the scenario's `fault.params` — read it before guessing.
Examples seen in built-in scenarios: `latency`, `packet_loss`,
`bandwidth` (network), `cpu_load` (cpu_stress), `memory_bytes`
(memory_stress).

While the run executes, do not poll Prometheus or Docker yourself —
the orchestrator owns the devnet. Just stream the runner's output.

---

## Step 5 — Locate report

```bash
LATEST_REPORT=$(ls -1t reports/test-*.json 2>/dev/null | head -1)
```

If `reports/` is empty after a run, that's a runner bug — surface it.

---

## Step 6 — Triage

Hand off to the `triage-report` skill:

```
Skill: triage-report
args: <absolute path to LATEST_REPORT>
```

If `triage-report` isn't available, triage manually using these
authoritative field names from `pkg/reporting/types.go`:

1. **Top-level `status` and `success`.** `success: true` ⇒ all critical
   criteria passed; `false` ⇒ at least one missed; check `errors[]` and
   `message` for pipeline aborts.
2. **`success_criteria[]`.** For each entry with `critical: true` and
   `passed: false`, quote `query`, `threshold`, `value`, and `message`.
3. **`cleanup_summary` + `cleanup_log[]`.** Any audit entry with
   `success: false` indicates orphaned tc rules, iptables chains, or
   sidecar containers — flag for manual cleanup.
4. **Pre-check vs post-check delta.** `post_fault_only: true` criteria
   are *expected* to fail before injection — that's the
   fault-effectiveness signal, not a bug.

Summarize: pass/fail headline, missed critical criteria with numbers,
any cleanup residue.

---

## Recovery — when a run fails (exit 1 or 2)

Before re-running:

1. **Inspect `cleanup_log` in the latest report.** Look for orphaned
   tc qdiscs, iptables chains (from `connection_drop` / `network`),
   chaos sidecars (`docker ps --filter name=chaos-`), or Envoy /
   corruption-proxy sidecars left attached to targets.
2. **Clean residue manually before retrying.** `pkg/injection/verification/`
   defines what each fault should leave behind (nothing).
3. **Exit code 2 (infra error):** experiment never started. Read the
   `InfraError` message in stderr, fix the cause (config, Kurtosis
   connectivity, Docker daemon), retry from Step 1.
4. **Exit code 1 (criteria failure):** experiment ran, the SUT missed a
   threshold. This is a real chaos finding, not a tooling bug. Triage
   first (Step 6) before deciding whether to re-run.
