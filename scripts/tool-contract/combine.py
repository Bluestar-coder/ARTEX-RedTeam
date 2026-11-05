#!/usr/bin/env python3
"""Merge the per-side JSONL dumps into one canonical, diff-friendly JSON.

Input:  agent-<side>.jsonl  (agent + sdk families) and server-<side>.jsonl (server + traffic).
Output: {"total": N, "by_family": {...}, "tools": [sorted stable contracts]}
"""
import json
import sys
from collections import Counter


def main(agent_path, server_path):
    tools = []
    for path in (agent_path, server_path):
        with open(path, encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if line:
                    tools.append(json.loads(line))
    # Deterministic order independent of construction order.
    tools.sort(key=lambda t: (t["family"], t["name"]))
    counts = Counter(t["family"] for t in tools)
    json.dump(
        {
            "total": len(tools),
            "by_family": dict(sorted(counts.items())),
            "tools": tools,
        },
        sys.stdout,
        ensure_ascii=False,
        sort_keys=True,
        indent=2,
    )
    sys.stdout.write("\n")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("usage: combine.py <agent.jsonl> <server.jsonl>")
    main(sys.argv[1], sys.argv[2])
