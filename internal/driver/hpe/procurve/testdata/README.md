# Captures — SW-PLAYER-79

Verbatim output of a **ProCurve 2610-48 (J9088A), software R.11.121**, read on
**2026-08-03** at 203.0.113.79 (a customer site) over SSH through a meshcraft
proxy. A production switch, not a lab unit.

## What has been done to these files, and what has not

The bytes are the device's, with exactly the transformation `stripControl`
(cli.go) applies before any parser sees them:

- `ESC E` → `\n` (this CLI's line terminator on prompts and echoes),
- CSI and other escape sequences removed,
- `\r` removed.

**Nothing else.** Trailing spaces, blank cells, alignment padding and the
device's own truncations are left exactly as they arrived — they are the point.
An earlier version of this connector passed its tests against a fixture whose
control characters a capture pipeline had already tidied, and then read 4647
characters and zero VLANs off the real switch.

## What each file is for

| File | What it pins |
| --- | --- |
| `show_interfaces_brief.txt` | ports 51-52 are EMPTY optical cages: blank Type and Mode. Split this on whitespace and link state comes from the wrong column |
| `show_spanning_tree.txt` | the `Multiple Spanning Tree (MST)` banner over a switch running RSTP; the bridge priority sharing a line with another label |
| `show_spanning_tree_config.txt` | labels carrying their default in brackets (`STP Enabled [No] : Yes`); the MAC-derived default region name |
| `show_ip.txt` + `show_vlans.txt` | the addressing table is keyed by VLAN NAME and never by id — the two views only join through `show vlans` |
| `show_lldp_info_local_device.txt` | the only place a session sees the model AND the part number |
| `show_lldp_info_remote_device.txt` | the device truncating a neighbour's port description to the column width (`ge-0/0...`) |
| `show_running_config.txt` | `no untagged 1-52` as a REMOVAL; `max-vlans 30`; a real 10-VLAN site plan |
| `show_snmp_server.txt` | `meshcraft-ro` at `Operator`, so the idempotent branch of `ApplySNMP` has something to recognise |
| `show_ip_ssh.txt` | the four-session table that explains this range's "refused pty" |
| `show_system_information.txt` | serial, base MAC and revision, two label pairs per line |
| `show_version.txt` | that this command carries neither model nor part number |
