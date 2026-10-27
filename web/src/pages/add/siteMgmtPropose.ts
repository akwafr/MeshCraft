/**
 * Derive soft-reset management addressing from the site's unified mgmt plan.
 * IPAM allocator is not built yet (docs): we suggest the next free address by
 * walking the meshcraft pool (or the subnet) and skipping gateway + used IPs.
 */

import type { MgmtPlan } from "../../api";

export type SiteMgmtProposal = {
  ip: string;
  mask: string;
  prefixLen: number;
  gateway: string;
  vlanId: number;
  untagged: boolean;
  /** CLI interface hint for FS S3900: "vlan N". */
  iface: string;
  subnet: string;
  source: MgmtPlan["source"];
  configured: boolean;
  /** Why this IP was chosen. */
  ipReason: string;
};

function parseIPv4(s: string): number | null {
  var parts = s.trim().split(".");
  if (parts.length !== 4) return null;
  var n = 0;
  var i = 0;
  for (i = 0; i < 4; i++) {
    var o = Number(parts[i]);
    if (!Number.isInteger(o) || o < 0 || o > 255) return null;
    n = (n << 8) + o;
  }
  return n >>> 0;
}

function formatIPv4(n: number): string {
  return [(n >>> 24) & 255, (n >>> 16) & 255, (n >>> 8) & 255, n & 255].join(".");
}

function prefixToMask(prefix: number): string {
  if (prefix < 0 || prefix > 32) return "255.255.255.0";
  var bits = prefix === 0 ? 0 : (~0 << (32 - prefix)) >>> 0;
  return formatIPv4(bits);
}

function parseCIDR(cidr: string): { network: number; prefix: number } | null {
  var s = cidr.trim();
  if (!s) return null;
  var slash = s.indexOf("/");
  if (slash < 0) return null;
  var ip = parseIPv4(s.slice(0, slash));
  var prefix = Number(s.slice(slash + 1));
  if (ip === null || !Number.isInteger(prefix) || prefix < 0 || prefix > 32) {
    return null;
  }
  var mask = prefix === 0 ? 0 : (~0 << (32 - prefix)) >>> 0;
  return { network: (ip & mask) >>> 0, prefix };
}

function collectUsed(usedIps: string[], gateway: string): Set<number> {
  var out = new Set<number>();
  var g = parseIPv4(gateway);
  if (g !== null) out.add(g);
  var i = 0;
  for (i = 0; i < usedIps.length; i++) {
    var n = parseIPv4(usedIps[i]);
    if (n !== null) out.add(n);
  }
  return out;
}

/**
 * Build a soft-reset proposal from the site plan and already-used management IPs.
 */
export function proposeSiteMgmt(
  plan: MgmtPlan | null | undefined,
  usedIps: string[],
): SiteMgmtProposal | null {
  if (!plan) return null;
  var subnet = (plan.subnet_v4 || "").trim();
  var parsed = parseCIDR(subnet);
  if (!parsed) {
    return {
      ip: "",
      mask: "",
      prefixLen: 0,
      gateway: plan.gateway_v4 || "",
      vlanId: plan.vlan_id || 1,
      untagged: !!plan.untagged,
      iface: `vlan ${plan.vlan_id || 1}`,
      subnet,
      source: plan.source,
      configured: !!plan.configured,
      ipReason: "missing_subnet",
    };
  }

  var mask = prefixToMask(parsed.prefix);
  var broadcast =
    parsed.prefix === 32
      ? parsed.network
      : (parsed.network | (~((~0 << (32 - parsed.prefix)) >>> 0) >>> 0)) >>> 0;
  var used = collectUsed(usedIps, plan.gateway_v4 || "");

  var start = parseIPv4(plan.pool_start || "");
  var end = parseIPv4(plan.pool_end || "");
  var rangeOk =
    plan.source === "meshcraft" &&
    start !== null &&
    end !== null &&
    start <= end;

  var lo: number;
  var hi: number;
  var reason: string;
  if (rangeOk) {
    lo = start!;
    hi = end!;
    reason = "pool";
  } else {
    // Host range inside the subnet, skip network & broadcast.
    lo = parsed.prefix >= 31 ? parsed.network : parsed.network + 1;
    hi = parsed.prefix >= 31 ? broadcast : broadcast - 1;
    reason = plan.source === "meshcraft" ? "pool_fallback_subnet" : "subnet";
  }

  var ip = "";
  var cur = lo;
  while (cur <= hi) {
    if (!used.has(cur) && cur !== parsed.network && cur !== broadcast) {
      ip = formatIPv4(cur);
      break;
    }
    cur++;
  }

  var vlanId = plan.vlan_id || 1;
  return {
    ip,
    mask,
    prefixLen: parsed.prefix,
    gateway: (plan.gateway_v4 || "").trim(),
    vlanId,
    untagged: !!plan.untagged,
    iface: `vlan ${vlanId}`,
    subnet,
    source: plan.source,
    configured: !!plan.configured,
    ipReason: ip ? reason : "exhausted",
  };
}
