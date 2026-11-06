---
Status: Accepted
Date: 2026-11-06
---

# ADR-39833 — The controller accepts sub-1024-bit RSA host keys from legacy switches

## Context

A ProCurve 2650 (J4899A, H.10.x) on a test bench answers SSH with an
**896-bit RSA host key**. Since Go 1.24, `crypto/rsa` refuses any key shorter
than 1024 bits, and the refusal happens during the key exchange — before a
credential is ever presented. Discovery reported it verbatim:

    crypto/rsa: 896-bit keys are insecure

so the switch could not be identified, adopted, read or reached from the
console, although telnet and the web interface were open and the SSH
algorithm lists of every connector already carry this vintage's
`diffie-hellman-group1-sha1`, `3des-cbc` and `ssh-dss`.

## Decision

The controller binary is built with `//go:debug rsa1024min=0`
(`cmd/controller/main.go`). Nothing else changes: the proxy, the MCP server and
the tooling keep Go's default.

## Consequences

- SSH to switches whose host key is under 1024 bits works again: discovery,
  connectors and the console.
- The setting is process-wide in the controller, so it also relaxes RSA
  verification elsewhere in that process (TLS to outside services included).
  meshcraft does not accept or issue such keys itself; the exposure is a peer
  presenting one, which is the legacy case this restores.
- Falling back to telnet was rejected: it would carry the administration
  password in clear on the management VLAN, which is worse than a weak host key
  on the same VLAN.
- When the last such switch leaves service, removing the directive is a
  one-line change.
