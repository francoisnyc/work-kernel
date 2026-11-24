"""Helpers for the second bake-off's scripts (Python 3 standard library only).

The plain-Temporal side is called through its HTTP API with the same dev
tokens as the kernel side. The kernel side and the shared model stub are
reached through the first spike's helpers, imported by path.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

SPIKE = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FIRST = os.path.join(os.path.dirname(SPIKE), "camunda-bakeoff")
sys.path.insert(0, os.path.join(FIRST, "scripts"))
from bakeoff import ask_model, pli, short  # noqa: E402,F401  (the first spike's helpers)

PLAIN = os.environ.get("PLAIN", "http://127.0.0.1:28080")


def plain(as_, method, path, body=None, token=None):
    """Call the plain-Temporal API as a dev user. Returns (status, JSON or text)."""
    req = urllib.request.Request(PLAIN + path, method=method, data=None if body is None else json.dumps(body).encode())
    if as_ or token:
        req.add_header("Authorization", "Bearer " + (token or f"dev-{as_}-not-a-secret"))
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            raw = r.read()
            return r.status, json.loads(raw) if raw else {}
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw)
        except ValueError:
            return e.code, raw.decode(errors="replace")


def wait_state(as_, rid, *states, timeout=20):
    """Poll a request until it is in one of states; return its row."""
    row = {}
    for _ in range(timeout * 5):
        st, row = plain(as_, "GET", f"/requests/{rid}")
        if st == 200 and row.get("state") in states:
            return row
        time.sleep(0.2)
    raise RuntimeError(f"{rid} did not reach {states}: {row}")
