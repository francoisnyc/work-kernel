"""Measure 4: the compromised agent, per process, on both sides.

Writes results/attacks-plain.md and results/attacks-kernel.md.
(results/attacks-plain-access-before-hardening.md is the access request's
list run on the code exactly as the second builder handed it back.)
"""
import os
import sys

import attacks_kernel
import attacks_plain
from pt import SPIKE

which = sys.argv[1] if len(sys.argv) > 1 else "all"
if which in ("plain", "all"):
    rec = attacks_plain.Recorder()
    attacks_plain.purchase(rec)
    attacks_plain.access(rec)
    rec.write(os.path.join(SPIKE, "results/attacks-plain.md"), "Compromised agent against plain Temporal, per process",
              "The agent's credentials: its API token (agent:prep-bot, role preparer).")
if which in ("kernel", "all"):
    rec = attacks_plain.Recorder()
    attacks_kernel.purchase(rec)
    attacks_kernel.access(rec)
    rec.write(os.path.join(SPIKE, "results/attacks-kernel.md"), "Compromised agent against the kernel, per process",
              "The agent's credentials: its token and every grant (every power; acting for itself, the manager, finance, the CFO, the owner, the requester). "
              "The purchase list is the first bake-off's, run unchanged.")
