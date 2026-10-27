---
tags: [study, proxy]
---

# Web pass-through access — driving "web only" devices

Two distinct needs for devices without SSH:

## 1. Automated configuration (the webform connector)

The `transport/webform` [[Connectors|driver]] drives the device's web UI **directly over HTTP** (not through a browser): reproducing the requests the browser makes (login → cookie/token → POST of forms / internal JSON endpoints).

- Engineering method: capture the exchanges of the original UI (devtools/HAR) once per platform, then code these sequences in the driver. Many "smart switches" (D-Link DGS-1210, TP-Link Easy Smart, Netgear GS3xx, Zyxel GS1900) actually have CGI/JSON endpoints that are fairly stable per family.
- The known pitfalls, to encapsulate in the webform building block: single admin session (the driver must log out cleanly), CSRF tokens, home-made password encryption in JS (to reimplement in Go), and "save" ≠ "apply" (two steps on many models).
- Last resort if a UI cannot be automated over pure HTTP (canvas login, obfuscated JS): headless browser **on the controller side** (chromedp) — to be avoided, costly and fragile ([[Hard points and risks]]). Special case outside HTTP: TP-Link Easy Smart can only be configured through a proprietary UDP protocol → dedicated RPC on the proxy side, provided for by the [[Controller-proxy protocol]].

## 2. Manual access to the device's UI (the real pass-through)

The user sometimes wants to open the switch's web page themselves (feature not covered, diagnosis) while the device is only reachable through the [[Proxy]].

**Mechanism**: per-session HTTP reverse proxy, through the tunnel.

```
browser ── https://ctrl.example/pt/{session}/... ── controller
                                      │ TCP dial via tunnel (ProxyHandle.DialTCP)
                                      ▼
                             device 10.1.2.3:80
```

- The user clicks "Open the web interface" on the device record → the controller creates a **pass-through session** (sliding 15 min TTL, dedicated [[RBAC]] right, audit event in the [[Event log]]).
- The controller acts as a reverse proxy: rewrites `Host`, strips the `/pt/{session}` prefix, rewrites `Location` headers and cookies (`Path`). Internal HTTPS with a self-signed cert: accepted by the controller (fingerprint pinned on first access); the user, for their part, stays on the controller's own TLS.
- **Accepted limits**: UIs with hard-coded absolute paths or internal WebSockets may break under a path prefix. Built-in plan B: serve the session on a **dedicated subdomain** (`pt-{session}.ctrl.example`, wildcard DNS+cert) — zero path rewriting, maximum compatibility. The prefix is the default (no DNS prerequisite), the subdomain is the clean option.
- Security: never automatic authentication to the device in this mode (meshcraft does not inject the admin password into a user session); it is a *transported* access, not a delegated one. Closing the session = closing the tunnelled connections.
