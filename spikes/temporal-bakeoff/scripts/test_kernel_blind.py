"""Measure 4 for the kernel's blind-built access request (kernel-blind/):
restart the kernel side on that folder alone, create its systems, and run
the same access-request list as on the first bake-off's Type.
Writes results/attacks-kernel-blind.md. Leaves the kernel side on that
folder: run ../camunda-bakeoff/scripts/kernel-down.sh afterwards.
"""
import os
import subprocess

import attacks_kernel
import attacks_plain
from pt import FIRST, SPIKE

subprocess.run([f"{FIRST}/scripts/kernel-down.sh"], capture_output=True)
subprocess.run([f"{FIRST}/scripts/kernel-up.sh"], check=True, capture_output=True,
               env=dict(os.environ, TYPES=f"{SPIKE}/kernel-blind/types"))
subprocess.run(["bash", f"{SPIKE}/kernel-blind/setup.sh"], check=True, capture_output=True)
rec = attacks_plain.Recorder()
attacks_kernel.access(rec)
rec.write(os.path.join(SPIKE, "results/attacks-kernel-blind.md"),
          "Compromised agent against the kernel's access request, written blind (kernel-blind/)",
          "The same list as results/attacks-kernel.md's access part, on the Types a separate builder wrote from the purchase Type and the business's request.")
