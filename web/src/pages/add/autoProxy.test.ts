/**
 * Auto probe-proxy resolution:
 *   node --experimental-strip-types src/pages/add/autoProxy.test.ts
 */
import assert from "node:assert/strict";
import { resolveAutoProxy } from "./autoProxy.ts";

var prefixes = [
  { proxy_id: "p16", proxy_name: "site-16", prefix: "10.99.0.0/16", enabled: true },
  { proxy_id: "p24", proxy_name: "site-24", prefix: "10.99.5.0/24", enabled: true },
  { proxy_id: "off", proxy_name: "disabled", prefix: "10.99.0.0/8", enabled: false },
  { proxy_id: "v6", proxy_name: "v6", prefix: "2001:db8::/32", enabled: true },
  { proxy_id: "other", proxy_name: "elsewhere", prefix: "192.168.1.0/24", enabled: true },
];

// A /16 declaration covers any address inside it (the /24 subnet case).
var hit = resolveAutoProxy("10.99.0.2", prefixes, null);
assert.ok(hit);
assert.equal(hit.proxyId, "p16");
assert.equal(hit.prefix, "10.99.0.0/16");

// Longest prefix wins when several ranges contain the address.
hit = resolveAutoProxy("10.99.5.7", prefixes, null);
assert.ok(hit);
assert.equal(hit.proxyId, "p24", "the /24 must beat the enclosing /16");

// Disabled prefixes never match (10.98.x is only inside the disabled /8).
assert.equal(resolveAutoProxy("10.98.0.1", prefixes, null), null);

// Restriction to the proxies the site actually sees.
hit = resolveAutoProxy("10.99.5.7", prefixes, new Set(["p16"]));
assert.ok(hit);
assert.equal(hit.proxyId, "p16", "non-allowed proxies are skipped");

// No declared range → null; garbage ip → null.
assert.equal(resolveAutoProxy("172.16.0.1", prefixes, null), null);
assert.equal(resolveAutoProxy("not-an-ip", prefixes, null), null);

// /0 catch-all still matches, with the lowest possible priority.
var withDefault = prefixes.concat([
  { proxy_id: "any", proxy_name: "catch-all", prefix: "0.0.0.0/0", enabled: true },
]);
hit = resolveAutoProxy("172.16.0.1", withDefault, null);
assert.ok(hit);
assert.equal(hit.proxyId, "any");
hit = resolveAutoProxy("10.99.5.7", withDefault, null);
assert.ok(hit);
assert.equal(hit.proxyId, "p24");

console.log("autoProxy.test.ts: ok");
