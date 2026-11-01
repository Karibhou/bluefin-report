#!/usr/bin/env python3
"""Serve only repository fixtures to the live screenshot application."""

from pathlib import Path
import sys


def main():
    args = sys.argv[1:]
    scope = "user" if "--user" in args else "system"
    fixtures = Path(__file__).resolve().parents[1] / "internal/snapshot/testdata"
    if "generate" in args:
        path = fixtures / f"generate-{scope}.json"
    elif "list-sources" in args:
        path = fixtures / f"list-sources-{scope}.json"
    elif "describe" in args:
        path = fixtures / "describe-system.seq"
    else:
        raise SystemExit("screenshot collector: unsupported invocation")
    sys.stdout.buffer.write(path.read_bytes())


if __name__ == "__main__":
    main()
