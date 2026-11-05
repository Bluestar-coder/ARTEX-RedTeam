#!/usr/bin/env python3
"""Extract every SQL string literal from Go sources into a stable snapshot.

Usage: sql_snapshot.py <repo-root> > snapshot.json

Each string literal (backtick raw string or interpreted string) that looks like
SQL is normalised (whitespace collapsed) and counted. The comparison is a
multiset, so moving a query between files or reordering code is invisible — only
changing SQL text changes the snapshot. This is what task B promises: parameters
may be rebound, but the statement structure must not change.
"""
import json
import os
import re
import sys

ROOT = os.path.abspath(sys.argv[1])
SQL_RE = re.compile(
    r"\b(?:SELECT|INSERT\s+INTO|UPDATE\s+\w|DELETE\s+FROM|ALTER\s+TABLE|CREATE\s+(?:TABLE|INDEX|OR\s+REPLACE\s+FUNCTION|TRIGGER)|"
    r"DROP\s+(?:TABLE|INDEX|TRIGGER|SCHEMA)|WITH\s+\w+\s+AS|TRUNCATE|GRANT|REVOKE)\b",
    re.IGNORECASE,
)

# Raw Go string literals: `...` (may span lines) and "..." literals with escapes.
RAW_RE = re.compile(r"`([^`]*)`")
STR_RE = re.compile(r'"((?:[^"\\\n]|\\.)*)"')


def normalize(s: str) -> str:
    s = s.replace("\\n", " ").replace("\\t", " ")
    return re.sub(r"\s+", " ", s).strip()


def main():
    counts = {}
    for dirpath, _dirnames, names in os.walk(ROOT):
        if any(part in dirpath for part in (".git", "node_modules", ".codegraph", "web")):
            continue
        for name in names:
            if not name.endswith(".go"):
                continue
            path = os.path.join(dirpath, name)
            try:
                text = open(path, encoding="utf-8").read()
            except OSError:
                continue
            # Strip Go comments so illustrative SQL in doc comments is not counted
            # as a query. Only whole-line // comments and /* */ blocks are removed;
            # inline // inside a raw string is left untouched.
            text = re.sub(r"(?m)^\s*//.*$", "", text)
            text = re.sub(r"(?s)/\*.*?\*/", "", text)
            for rx in (RAW_RE, STR_RE):
                for m in rx.finditer(text):
                    lit = normalize(m.group(1))
                    if lit and SQL_RE.search(lit):
                        counts[lit] = counts.get(lit, 0) + 1
    out = {
        "sql_literals": [{"sql": k, "count": counts[k]} for k in sorted(counts)],
        "distinct": len(counts),
        "total": sum(counts.values()),
    }
    json.dump(out, sys.stdout, ensure_ascii=False, indent=2, sort_keys=True)
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
