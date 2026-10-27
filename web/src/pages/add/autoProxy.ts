/**
 * "Auto" probe-proxy mode: pick the proxy whose DECLARED range contains the
 * management address the wizard is about to probe.
 *
 * Containment, not equality: a /24 management subnet is covered by a proxy
 * that declares the enclosing /16. When several ranges contain the address,
 * the MOST SPECIFIC (longest prefix) wins — the proxy that declared the
 * tighter range is the one an operator meant for that network.
 */

export type AutoProxyPrefix = {
  proxy_id: string;
  proxy_name?: string;
  prefix: string;
  enabled: boolean;
};

export type AutoProxyChoice = {
  proxyId: string;
  proxyName: string;
  prefix: string;
};

function parseIPv4(s: string): number | null {
  var parts = s.trim().split(".");
  if (parts.length !== 4) return null;
  var n = 0;
  var i = 0;
  for (i = 0; i < 4; i++) {
    if (!/^\d+$/.test(parts[i])) return null;
    var o = Number(parts[i]);
    if (o < 0 || o > 255) return null;
    n = (n << 8) + o;
  }
  return n >>> 0;
}

function parseCIDRv4(cidr: string): { network: number; prefixLen: number } | null {
  var s = cidr.trim();
  var slash = s.indexOf("/");
  if (slash < 0) return null;
  var ip = parseIPv4(s.slice(0, slash));
  var prefixLen = Number(s.slice(slash + 1));
  if (ip === null || !Number.isInteger(prefixLen) || prefixLen < 0 || prefixLen > 32) {
    return null;
  }
  var mask = prefixLen === 0 ? 0 : (~0 << (32 - prefixLen)) >>> 0;
  return { network: (ip & mask) >>> 0, prefixLen };
}

function containsV4(net: { network: number; prefixLen: number }, ip: number): boolean {
  var mask = net.prefixLen === 0 ? 0 : (~0 << (32 - net.prefixLen)) >>> 0;
  return ((ip & mask) >>> 0) === net.network;
}

/**
 * Resolve the proxy for `ip` among declared prefixes. `allowedProxyIds`
 * restricts to proxies the caller may actually use (those the site sees);
 * pass null to accept any. Disabled and non-IPv4 prefixes are ignored.
 */
export function resolveAutoProxy(
  ip: string,
  prefixes: AutoProxyPrefix[],
  allowedProxyIds: Set<string> | null,
): AutoProxyChoice | null {
  var target = parseIPv4(ip);
  if (target === null) return null;

  var best: AutoProxyChoice | null = null;
  var bestLen = -1;
  var i = 0;
  for (i = 0; i < prefixes.length; i++) {
    var p = prefixes[i];
    if (!p.enabled) continue;
    if (allowedProxyIds !== null && !allowedProxyIds.has(p.proxy_id)) continue;
    var net = parseCIDRv4(p.prefix);
    if (net === null) continue;
    if (!containsV4(net, target)) continue;
    if (net.prefixLen > bestLen) {
      bestLen = net.prefixLen;
      best = {
        proxyId: p.proxy_id,
        proxyName: p.proxy_name || p.proxy_id.slice(0, 8),
        prefix: p.prefix.trim(),
      };
    }
  }
  return best;
}
