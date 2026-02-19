#!/usr/bin/env python3
"""
generate_scenarios.py - Chaos scenario generator for Polygon PoS networks.

Generates scenario YAML files from a parameter matrix using full combinatorial
coverage (default) or pairwise/all-pairs reduction (--pairwise flag).

Parameter dimensions
--------------------
  fault_type  (11): packet_loss, latency, bandwidth_throttle, packet_reorder,
                    connection_drop, dns_latency, container_restart,
                    container_pause, cpu_stress, memory_pressure, disk_io
  target_tier  (8): validator1_heimdall, validator1_bor, validator1_both,
                    all_heimdall, all_bor, all_both, rabbitmq, rpc_nodes
  severity     (3): mild, moderate, severe

Full combinations: 11 × 8 × 3 = 264
Pairwise (t=2):    ~88  (all 2-way interactions covered)

Scenario names encode the actual test value, not an abstract level:
  gen-packet-loss-40pct-validator1-heimdall
  gen-container-restart-immediate-all-both
  gen-memory-pressure-1gb-rabbitmq

All generated scenarios use topology-safe success criteria (ratio expressions)
that hold for any N-validator network, so they never need updating when the
enclave size changes.

Dependencies: PyYAML  (pip install pyyaml)
              allpairspy (pip install allpairspy) -- only needed for --pairwise

Usage
-----
    python3 scripts/generate_scenarios.py                       # 264 scenarios
    python3 scripts/generate_scenarios.py --pairwise            # ~24 scenarios
    python3 scripts/generate_scenarios.py --list                # preview table
    python3 scripts/generate_scenarios.py --output ./gen/
    python3 scripts/generate_scenarios.py --enclave my-enclave  # embed enclave
"""

import argparse
import itertools
import os
import sys
from typing import Any

try:
    import yaml
except ImportError:
    sys.exit("PyYAML is required: pip install pyyaml")


# ─── Fault definitions ────────────────────────────────────────────────────────
# Each entry:  type (runner fault type), category (output subdirectory),
#              mild/moderate/severe (concrete params dict).

FAULT_DEFINITIONS: dict[str, dict] = {
    "packet_loss": {
        "type":     "network",
        "category": "network",
        "label": "Packet loss — simulates congested / lossy network links",
        "mild":     {"packet_loss": 10,    "target_proto": "tcp,udp", "device": "eth0"},
        "moderate": {"packet_loss": 40,    "target_proto": "tcp,udp", "device": "eth0"},
        "severe":   {"packet_loss": 80,    "target_proto": "tcp,udp", "device": "eth0"},
    },
    "latency": {
        "type":     "network",
        "category": "network",
        "label": "Added network latency — simulates slow / geographically distant peers",
        "mild":     {"latency": 100,  "target_proto": "tcp,udp", "device": "eth0"},
        "moderate": {"latency": 500,  "target_proto": "tcp,udp", "device": "eth0"},
        "severe":   {"latency": 2000, "target_proto": "tcp,udp", "device": "eth0"},
    },
    "bandwidth_throttle": {
        "type":     "network",
        "category": "network",
        "label": "Bandwidth throttle — simulates underpowered or constrained uplinks",
        "mild":     {"bandwidth": 10000, "target_proto": "tcp,udp", "device": "eth0"},
        "moderate": {"bandwidth": 1000,  "target_proto": "tcp,udp", "device": "eth0"},
        "severe":   {"bandwidth": 100,   "target_proto": "tcp,udp", "device": "eth0"},
    },
    "packet_reorder": {
        "type":     "network",
        "category": "network",
        # TC reorder requires a base latency so packets have time to be reordered.
        "label": "Packet reordering — simulates out-of-order delivery disrupting protocol sequencing",
        "mild":     {"reorder": 25, "latency": 10, "target_proto": "tcp,udp", "device": "eth0"},
        "moderate": {"reorder": 50, "latency": 10, "target_proto": "tcp,udp", "device": "eth0"},
        "severe":   {"reorder": 75, "latency": 10, "target_proto": "tcp,udp", "device": "eth0"},
    },
    "connection_drop": {
        "type":     "connection_drop",
        "category": "network",
        "label": "Connection drop (iptables) — simulates intermittent TCP session resets",
        "mild":     {"probability": 0.1, "target_proto": "tcp", "rule_type": "drop"},
        "moderate": {"probability": 0.4, "target_proto": "tcp", "rule_type": "drop"},
        "severe":   {"probability": 0.8, "target_proto": "tcp", "rule_type": "drop"},
    },
    "dns_latency": {
        "type":     "dns",
        "category": "network",
        "label": "DNS latency / failure — simulates slow or flaky service discovery",
        "mild":     {"delay_ms": 250,  "failure_rate": 0.0},
        "moderate": {"delay_ms": 1000, "failure_rate": 0.1},
        "severe":   {"delay_ms": 5000, "failure_rate": 0.3},
    },
    "container_restart": {
        "type":     "container_restart",
        "category": "applications",
        "label": "Container restart — simulates process crash and recovery",
        "mild":     {"grace_period": 30},
        "moderate": {"grace_period": 5},
        "severe":   {"grace_period": 0},
    },
    "container_pause": {
        "type":     "container_pause",
        "category": "applications",
        # 'duration' here is the pause window, distinct from the scenario duration.
        "label": "Container pause (SIGSTOP) — simulates frozen process / GC pause / OOM stall",
        "mild":     {"duration": "30s",  "unpause": True},
        "moderate": {"duration": "60s",  "unpause": True},
        "severe":   {"duration": "120s", "unpause": True},
    },
    "cpu_stress": {
        "type":     "cpu_stress",
        "category": "cpu-memory",
        "label": "CPU stress — simulates CPU-bound workload competing with validator logic",
        "mild":     {"cpu_percent": 50},
        "moderate": {"cpu_percent": 75},
        "severe":   {"cpu_percent": 95},
    },
    "memory_pressure": {
        "type":     "memory_stress",
        "category": "cpu-memory",
        "label": "Memory pressure — simulates memory-constrained environment / memory leak",
        "mild":     {"memory_mb": 256},
        "moderate": {"memory_mb": 512},
        "severe":   {"memory_mb": 1024},
    },
    "disk_io": {
        "type":     "disk_io",
        "category": "filesystem",
        "label": "Disk I/O delay — simulates slow storage (HDD / NFS / overloaded SSD)",
        "mild":     {"io_latency_ms": 100,  "operation": "all"},
        "moderate": {"io_latency_ms": 500,  "operation": "all"},
        "severe":   {"io_latency_ms": 2000, "operation": "all"},
    },
}

# ─── Severity labels and name slugs ─────────────────────────────────────────
# SEVERITY_LABELS: used in the human-readable description and --list output.
# SEVERITY_SLUGS:  used in the scenario filename/name — replaces mild/moderate/severe
#                  with the actual test value so names are self-documenting.

SEVERITY_SLUGS: dict[str, dict[str, str]] = {
    "packet_loss":        {"mild": "10pct",     "moderate": "40pct",      "severe": "80pct"},
    "latency":            {"mild": "100ms",      "moderate": "500ms",      "severe": "2s"},
    "bandwidth_throttle": {"mild": "10mbps",     "moderate": "1mbps",      "severe": "100kbps"},
    "packet_reorder":     {"mild": "25pct",      "moderate": "50pct",      "severe": "75pct"},
    "connection_drop":    {"mild": "10pct",      "moderate": "40pct",      "severe": "80pct"},
    "dns_latency":        {"mild": "250ms",       "moderate": "1s",         "severe": "5s"},
    "container_restart":  {"mild": "graceful",   "moderate": "quick",      "severe": "immediate"},
    "container_pause":    {"mild": "30s",         "moderate": "60s",        "severe": "120s"},
    "cpu_stress":         {"mild": "50pct",      "moderate": "75pct",      "severe": "95pct"},
    "memory_pressure":    {"mild": "256mb",       "moderate": "512mb",      "severe": "1gb"},
    "disk_io":            {"mild": "100ms",       "moderate": "500ms",      "severe": "2s"},
}

SEVERITY_LABELS: dict[str, dict[str, str]] = {
    "packet_loss":        {"mild": "10% packet loss",      "moderate": "40% packet loss",     "severe": "80% packet loss"},
    "latency":            {"mild": "100 ms added latency",  "moderate": "500 ms added latency", "severe": "2 s added latency"},
    "bandwidth_throttle": {"mild": "10 Mbps cap",           "moderate": "1 Mbps cap",           "severe": "100 kbps cap"},
    "packet_reorder":     {"mild": "25% reorder",           "moderate": "50% reorder",          "severe": "75% reorder"},
    "connection_drop":    {"mild": "10% TCP drop prob.",    "moderate": "40% TCP drop prob.",   "severe": "80% TCP drop prob."},
    "dns_latency":        {"mild": "250 ms DNS delay",      "moderate": "1 s DNS delay",        "severe": "5 s DNS delay / 30% fail"},
    "container_restart":  {"mild": "graceful (30 s grace)", "moderate": "fast (5 s grace)",     "severe": "immediate (0 s grace)"},
    "container_pause":    {"mild": "30 s pause",            "moderate": "60 s pause",           "severe": "120 s pause"},
    "cpu_stress":         {"mild": "50% CPU load",          "moderate": "75% CPU load",         "severe": "95% CPU load"},
    "memory_pressure":    {"mild": "256 MB pressure",       "moderate": "512 MB pressure",      "severe": "1 GB pressure"},
    "disk_io":            {"mild": "100 ms I/O delay",      "moderate": "500 ms I/O delay",     "severe": "2 s I/O delay"},
}

# ─── Target tiers ─────────────────────────────────────────────────────────────
# Explicit patterns narrow to a single known validator; wildcard patterns
# auto-discover all matching services so they work on any N-validator setup.

TARGET_TIERS: dict[str, dict] = {
    "validator1_heimdall": {
        "label": "validator 1 — Heimdall consensus layer only",
        "selectors": [
            {"pattern": r"l2-cl-1-heimdall-v2-bor-validator", "alias": "target_heimdall"},
        ],
    },
    "validator1_bor": {
        "label": "validator 1 — Bor execution layer only",
        "selectors": [
            {"pattern": r"l2-el-1-bor-heimdall-v2-validator", "alias": "target_bor"},
        ],
    },
    "validator1_both": {
        "label": "validator 1 — Heimdall + Bor (both layers of one validator)",
        "selectors": [
            {"pattern": r"l2-cl-1-heimdall-v2-bor-validator", "alias": "target_heimdall"},
            {"pattern": r"l2-el-1-bor-heimdall-v2-validator", "alias": "target_bor"},
        ],
    },
    "all_heimdall": {
        "label": "all validators — Heimdall consensus layer (wildcard)",
        "selectors": [
            {"pattern": r"l2-cl-.*-heimdall-v2-bor-validator", "alias": "target_heimdall"},
        ],
    },
    "all_bor": {
        "label": "all validators — Bor execution layer (wildcard)",
        "selectors": [
            {"pattern": r"l2-el-.*-bor-heimdall-v2-validator", "alias": "target_bor"},
        ],
    },
    "all_both": {
        "label": "all validators — Heimdall + Bor both layers (wildcard)",
        "selectors": [
            {"pattern": r"l2-cl-.*-heimdall-v2-bor-validator", "alias": "target_heimdall"},
            {"pattern": r"l2-el-.*-bor-heimdall-v2-validator", "alias": "target_bor"},
        ],
    },
    "rabbitmq": {
        "label": "RabbitMQ message broker (Heimdall event bus)",
        "selectors": [
            {"pattern": r"l2-cl-.*-rabbitmq", "alias": "target_rabbitmq"},
        ],
    },
    "rpc_nodes": {
        "label": "Bor RPC-only nodes (non-validator full nodes)",
        "selectors": [
            {"pattern": r"l2-el-.*-bor-heimdall-v2-rpc", "alias": "target_rpc"},
        ],
    },
}

SEVERITIES = ["mild", "moderate", "severe"]

# Duration scales with severity: more disruptive faults get longer windows.
TIMING: dict[str, dict] = {
    "mild":     {"duration": "3m", "warmup": "30s", "cooldown": "30s"},
    "moderate": {"duration": "5m", "warmup": "60s", "cooldown": "60s"},
    "severe":   {"duration": "8m", "warmup": "2m",  "cooldown": "2m"},
}


# ─── Topology-safe success criteria ──────────────────────────────────────────
# Expressed as ratios so they hold for any N-validator enclave.

INVARIANT_CRITERIA = [
    {
        "name": "block_production_continues",
        "description": "Network continues producing Bor blocks during the fault",
        "type": "prometheus",
        "query": 'increase(chain_head_block{job=~"l2-el-.*-bor-heimdall-v2-validator"}[1m])',
        "threshold": "> 0",
        "critical": True,
    },
    {
        "name": "consensus_height_advances",
        "description": "Heimdall consensus height continues to increase",
        "type": "prometheus",
        "query": 'increase(cometbft_consensus_height{job=~"l2-cl-.*-heimdall-v2-bor-validator"}[1m])',
        "threshold": "> 0",
        "critical": True,
    },
    {
        "name": "bft_quorum_maintained",
        "description": "At least 2/3 of validators remain online (topology-safe BFT quorum ratio)",
        "type": "prometheus",
        "query": (
            'count(up{job=~"l2-cl-.*-heimdall-v2-bor-validator"} == 1)'
            ' / scalar(count(up{job=~"l2-cl-.*-heimdall-v2-bor-validator"}))'
        ),
        "threshold": ">= 0.67",
        "critical": True,
    },
]


# ─── Scenario builder ─────────────────────────────────────────────────────────

def scenario_description(fault_type: str, target_tier: str, severity: str) -> str:
    fault_label  = SEVERITY_LABELS[fault_type][severity]
    target_label = TARGET_TIERS[target_tier]["label"]
    fault_ctx    = FAULT_DEFINITIONS[fault_type]["label"]
    return f"{fault_label} on {target_label}. {fault_ctx}."


def build_scenario(fault_type: str, target_tier: str, severity: str) -> dict[str, Any]:
    fault_def   = FAULT_DEFINITIONS[fault_type]
    tier        = TARGET_TIERS[target_tier]
    timing      = TIMING[severity]
    selectors   = tier["selectors"]

    # Fresh copy per fault so PyYAML does not emit &id001/*id001 anchors.
    base_params = {k: v for k, v in fault_def[severity].items()}
    faults = [
        {
            "phase":       f"{fault_type.replace('_', '-')}-{s['alias']}",
            "description": f"{SEVERITY_LABELS[fault_type][severity]} applied to {s['alias']}",
            "target":      s["alias"],
            "type":        fault_def["type"],
            "params":      dict(base_params),
        }
        for s in selectors
    ]

    criteria = [{**c, "window": timing["duration"]} for c in INVARIANT_CRITERIA]

    slug = SEVERITY_SLUGS[fault_type][severity]
    name = (
        f"gen"
        f"-{fault_type.replace('_', '-')}"
        f"-{slug}"
        f"-{target_tier.replace('_', '-')}"
    )

    return {
        "apiVersion": "chaos.polygon.io/v1",
        "kind": "ChaosScenario",
        "metadata": {
            "name": name,
            "description": scenario_description(fault_type, target_tier, severity),
            "tags": ["generated", fault_type.replace("_", "-"), slug, target_tier],
            "author": "scripts/generate_scenarios.py",
            "version": "0.0.1",
        },
        "spec": {
            "targets": [
                {
                    "selector": {
                        "type":    "kurtosis_service",
                        "enclave": "${ENCLAVE_NAME}",
                        "pattern": s["pattern"],
                    },
                    "alias": s["alias"],
                }
                for s in selectors
            ],
            "duration": timing["duration"],
            "warmup":   timing["warmup"],
            "cooldown": timing["cooldown"],
            "faults": faults,
            "success_criteria": criteria,
            "metrics": [
                "chain_head_block",
                "cometbft_consensus_height",
                "cometbft_consensus_validators",
                "up",
            ],
        },
    }


# ─── Combination strategies ───────────────────────────────────────────────────

def full_combinations() -> list[tuple[str, str, str]]:
    return list(itertools.product(
        FAULT_DEFINITIONS.keys(),
        TARGET_TIERS.keys(),
        SEVERITIES,
    ))


def pairwise_combinations() -> list[tuple[str, str, str]]:
    """
    Greedy pairwise (t=2) reduction.

    Falls back to a built-in IPOG-style greedy algorithm when allpairspy is
    not installed. Both produce complete 2-way coverage.
    """
    dimensions = [
        list(FAULT_DEFINITIONS.keys()),
        list(TARGET_TIERS.keys()),
        SEVERITIES,
    ]
    try:
        from allpairspy import AllPairs
        return [tuple(row.test_vector) for row in AllPairs(dimensions)]
    except ImportError:
        pass

    # Built-in greedy implementation.
    all_combos = list(itertools.product(*dimensions))
    n = len(dimensions)

    required: set[tuple] = {
        (i, vi, j, vj)
        for i in range(n)
        for j in range(i + 1, n)
        for vi in dimensions[i]
        for vj in dimensions[j]
    }

    covered: set[tuple] = set()
    selected: list[tuple] = []

    while covered < required:
        best_combo, best_new = None, -1
        for combo in all_combos:
            new = sum(
                1
                for i in range(n)
                for j in range(i + 1, n)
                if (i, combo[i], j, combo[j]) not in covered
            )
            if new > best_new:
                best_new, best_combo = new, combo
        if best_combo is None or best_new == 0:
            break
        selected.append(best_combo)
        for i in range(n):
            for j in range(i + 1, n):
                covered.add((i, best_combo[i], j, best_combo[j]))

    return selected


# ─── YAML serialisation ───────────────────────────────────────────────────────

def _str_representer(dumper, data):
    # Force single-quote style for strings that start with PromQL operators so
    # they are not misread as YAML scalars (>, <, =, >=, etc.).
    if data and data[0] in (">", "<", "=", "!"):
        return dumper.represent_scalar("tag:yaml.org,2002:str", data, style="'")
    return dumper.represent_scalar("tag:yaml.org,2002:str", data)


def _build_dumper():
    d = yaml.Dumper
    d.add_representer(str, _str_representer)
    return d


def to_yaml(scenario: dict) -> str:
    return yaml.dump(
        scenario,
        Dumper=_build_dumper(),
        default_flow_style=False,
        allow_unicode=True,
        sort_keys=False,
    )


# ─── CLI ──────────────────────────────────────────────────────────────────────

def main() -> None:
    ap = argparse.ArgumentParser(
        description="Generate chaos scenarios from a parameter matrix.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    ap.add_argument("--pairwise",  action="store_true",
                    help="Pairwise (t=2) reduction instead of full combinations.")
    ap.add_argument("--list",      action="store_true", dest="list_only",
                    help="Preview scenario table without writing files.")
    ap.add_argument("--output",    default="generated/polygon-chain",
                    help="Output root directory (default: generated/polygon-chain).")
    ap.add_argument("--enclave",   default=None,
                    help="Substitute this value for ${ENCLAVE_NAME} in output files.")
    args = ap.parse_args()

    combos   = pairwise_combinations() if args.pairwise else full_combinations()
    strategy = "pairwise" if args.pairwise else "full combinatorial"

    print(f"Strategy   : {strategy}")
    print(f"Combinations: {len(combos)}")
    print()

    if args.list_only:
        _print_list(combos)
        return

    written = 0
    for ft, tt, sv in combos:
        scenario = build_scenario(ft, tt, sv)
        content  = to_yaml(scenario)

        if args.enclave:
            content = content.replace("${ENCLAVE_NAME}", args.enclave)

        category = FAULT_DEFINITIONS[ft]["category"]
        out_dir  = os.path.join(args.output, category)
        os.makedirs(out_dir, exist_ok=True)

        name     = scenario["metadata"]["name"]
        out_path = os.path.join(out_dir, f"{name}.yaml")
        with open(out_path, "w") as f:
            f.write(content)
        print(f"  wrote  {out_path}")
        written += 1

    print()
    print(f"Generated {written} scenario(s) in {args.output}/")
    print()
    print("Run one:  ./bin/chaos-runner run "
          f"--scenario {args.output}/<category>/<name>.yaml --enclave <enclave>")


def _print_list(combos: list[tuple[str, str, str]]) -> None:
    # Columns: index, scenario name, what it tests, category, duration
    rows = []
    for ft, tt, sv in combos:
        slug     = SEVERITY_SLUGS[ft][sv]
        name     = f"gen-{ft.replace('_','-')}-{slug}-{tt.replace('_','-')}"
        desc     = f"{SEVERITY_LABELS[ft][sv]} on {TARGET_TIERS[tt]['label']}"
        category = FAULT_DEFINITIONS[ft]["category"]
        dur      = TIMING[sv]["duration"]
        rows.append((name, desc, category, dur))

    # Dynamic column widths.
    w_name = max(len(r[0]) for r in rows)
    w_desc = max(len(r[1]) for r in rows)
    w_cat  = max(len(r[2]) for r in rows)

    header = (
        f"{'#':<5}  "
        f"{'scenario name':<{w_name}}  "
        f"{'what it tests':<{w_desc}}  "
        f"{'category':<{w_cat}}  dur"
    )
    print(header)
    print("─" * len(header))
    for idx, (name, desc, cat, dur) in enumerate(rows, 1):
        print(
            f"{idx:<5}  "
            f"{name:<{w_name}}  "
            f"{desc:<{w_desc}}  "
            f"{cat:<{w_cat}}  {dur}"
        )


if __name__ == "__main__":
    main()
