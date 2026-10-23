"""Fail CI if core PG tests did not run, including a successful but skipped suite."""
import json
import sys
from pathlib import Path

events = [json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines() if line.strip()]
required = {
    ("github.com/Autumn-27/artex/db", "TestOpenSeed"),
    ("github.com/Autumn-27/artex/db", "TestInsertSettingIfAbsentDoesNotOverwrite"),
    ("github.com/Autumn-27/artex/server", "TestCoreTaskLifecyclePG"),
}
passed = {(e.get("Package"), e.get("Test")) for e in events if e.get("Action") == "pass"}
missing = required - passed
db_errors = [e.get("Output", "").strip() for e in events
             if any(s in e.get("Output", "").lower() for s in ("postgres unavailable", "no database config"))]
if missing or db_errors:
    raise SystemExit(f"Database validation incomplete: missing={sorted(missing)}, errors={db_errors}")
print("Required PostgreSQL integration tests passed.")
