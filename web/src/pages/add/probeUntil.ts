/**
 * Poll discovery probe until the management address is reachable (and ideally login_ok).
 */

import { post, type Discovered, type ProbeResult } from "../../api";

export type ProbeUntilOpts = {
  ip: string;
  /** One-off credential (when not using credential_id). */
  username?: string;
  password?: string;
  /** Preferred: instance credential tried server-side (secret stays on controller). */
  credentialId?: string;
  driver?: string;
  proxyId?: string;
  intervalMs?: number;
  timeoutMs?: number;
  log?: (msg: string) => void;
  signal?: AbortSignal;
  /** Prefer login_ok; if false, reachable alone is enough. Default true. */
  requireLogin?: boolean;
  /** Awaited before each attempt — "pause provisioning" hooks in here. */
  gate?: () => Promise<void>;
};

export type ProbeUntilResult = {
  candidate: Discovered;
  probe: ProbeResult;
};

export async function probeUntilReachable(
  opts: ProbeUntilOpts,
): Promise<ProbeUntilResult> {
  var intervalMs = opts.intervalMs ?? 8000;
  var timeoutMs = opts.timeoutMs ?? 180000;
  var requireLogin = opts.requireLogin !== false;
  var deadline = Date.now() + timeoutMs;
  var attempt = 0;

  while (Date.now() < deadline) {
    if (opts.gate) {
      // Paused time must not consume the probe budget.
      var pausedAt = Date.now();
      await opts.gate();
      deadline += Date.now() - pausedAt;
    }
    if (opts.signal?.aborted) throw new Error("probe aborted");
    attempt++;
    opts.log?.(`probe ${opts.ip} (attempt ${attempt})…`);
    try {
      var r = await post<{ candidate: Discovered; probe: ProbeResult }>(
        "/api/discovery/probe",
        {
          ip: opts.ip.trim(),
          proxy_id: opts.proxyId || "",
          username: opts.credentialId ? "" : opts.username || "",
          password: opts.credentialId ? "" : opts.password || "",
          credential_id: opts.credentialId || "",
          driver: opts.driver || "",
          fetch_config: true,
        },
      );
      var ok =
        r.probe.reachable && (!requireLogin || r.probe.login_ok);
      opts.log?.(
        `  reachable=${r.probe.reachable} login_ok=${r.probe.login_ok} driver=${r.probe.driver}`,
      );
      if (ok) return r;
    } catch (e) {
      opts.log?.(`  probe error: ${(e as Error).message || e}`);
    }
    var remaining = deadline - Date.now();
    if (remaining <= 0) break;
    await new Promise((resolve) =>
      setTimeout(resolve, Math.min(intervalMs, remaining)),
    );
  }
  throw new Error(
    `management address ${opts.ip} not reachable within ${Math.round(timeoutMs / 1000)}s`,
  );
}
