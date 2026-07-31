#!/usr/bin/env python3
import re
from pathlib import Path
ACCENT = re.compile(r"[àâäéèêëïîôùûüçÀÂÄÉÈÊËÏÎÔÙÛÜÇ]")
files = [
    "watch_handlers.go", "trapdest.go", "portevents.go", "lldp.go",
    "lldp_test.go", "trapdest_test.go", "proxyscope_test.go",
    "devices_collector_test.go", "devicecreds_handlers.go",
]
for f in files:
    t = Path("/srv/meshcraft/internal/api", f).read_text(encoding="utf-8")
    print(f"{f}: {len(ACCENT.findall(t))}")
