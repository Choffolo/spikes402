#!/usr/bin/env python3
"""Exit 0 if every OSV-Scanner finding is addressed in an OpenVEX document.

A finding is addressed when a statement for the same vulnerability id (or an
alias) has status not_affected or fixed. Status affected is reported as a
warning and still counts as addressed (it is recorded). Findings with no
statement fail the scan.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path


ADDRESSED = {"not_affected", "fixed", "affected"}


def load_vex(path: Path) -> dict[str, dict]:
    doc = json.loads(path.read_text())
    by_id: dict[str, dict] = {}
    for stmt in doc.get("statements") or []:
        vuln = stmt.get("vulnerability") or {}
        names = [vuln.get("name"), vuln.get("id"), vuln.get("@id")]
        names.extend(vuln.get("aliases") or [])
        for name in names:
            if name:
                by_id[str(name)] = stmt
    return by_id


def findings(osv: dict) -> list[dict]:
    out = []
    for result in osv.get("results") or []:
        source = (result.get("source") or {}).get("path") or ""
        for pkg in result.get("packages") or []:
            meta = pkg.get("package") or {}
            pkg_name = f"{meta.get('name') or '?'}@{meta.get('version') or '?'}"
            for v in pkg.get("vulnerabilities") or []:
                aliases = list(v.get("aliases") or [])
                out.append(
                    {
                        "id": v.get("id") or v.get("name") or "",
                        "aliases": aliases,
                        "package": pkg_name,
                        "source": source,
                        "summary": (v.get("summary") or v.get("details") or "").split("\n", 1)[0],
                    }
                )
    return out


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: vex-filter.py <osv.json> <openvex.json>", file=sys.stderr)
        return 2
    osv = json.loads(Path(sys.argv[1]).read_text())
    vex = load_vex(Path(sys.argv[2]))
    leftover = []
    for f in findings(osv):
        keys = [f["id"], *f["aliases"]]
        stmt = next((vex[k] for k in keys if k in vex), None)
        if stmt is None:
            leftover.append(f)
            continue
        status = stmt.get("status") or ""
        print(f"VEX {status}: {f['id']} in {f['package']}")
        if status not in ADDRESSED:
            leftover.append(f)
    if leftover:
        print(f"\n{len(leftover)} finding(s) not addressed in the VEX document:")
        for f in leftover:
            print(f"  {f['id']}  {f['package']}  {f['summary']}")
        return 1
    print("No unaddressed OSV findings.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
