#!/usr/bin/env python3
"""Fail CI if the required database integration tests did not actually pass."""

import json
import sys
from pathlib import Path


REQUIRED = {
    "akhflow.ai/akhflow/internal/store": {
        "TestAkhcheckImportDatabase",
        "TestBitrixImportConcurrentAndProjectIsolation",
        "TestEmailQueueIntegration",
    },
    "akhflow.ai/akhflow/internal/server": {
        "TestImportAkhcheckHTTP",
        "TestBitrixImportHTTP",
        "TestEmailSettingsAndStatusChanges",
    },
    "akhflow.ai/akhflow/internal/testdb": {"TestSchemaIsolationAndCleanup"},
}


def main():
    results = {}
    for line in Path(sys.argv[1]).read_text().splitlines():
        event = json.loads(line)
        action = event.get("Action")
        if action in {"pass", "skip", "fail"}:
            results[(event["Package"], event.get("Test"))] = action
    failures = []
    for package, names in REQUIRED.items():
        for name in sorted(names):
            result = results.get((package, name), "missing")
            if result != "pass":
                failures.append(f"{package}.{name}: {result}")
    if failures:
        for failure in failures:
            print(f"FAIL: {failure}")
        return 1
    count = sum(len(names) for names in REQUIRED.values())
    print(f"PASS: all {count} required database integration tests executed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
