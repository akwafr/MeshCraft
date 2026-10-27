/**
 * Ports that came UP slower than they should have.
 *
 * A leaf module with no imports, for the reason `portLabel.ts` is one: it can
 * then be exercised by the framework-free node suite, and the judgement here —
 * which speeds count as low, and what "up" means — is exactly the kind that
 * should be asserted rather than read.
 *
 * # WHY THIS IS WORTH A TILE
 *
 * A gigabit port negotiating at 100 Mbit/s is almost never a deliberate
 * configuration. It is a damaged pair in a patch lead, a cable longer or older
 * than anyone remembers, a mis-crimped keystone, or a fixed-speed setting left
 * behind on the far end. The port WORKS — it passes traffic, its link light is
 * on, it looks healthy on every other screen in this product — so nothing
 * surfaces it until somebody wonders why one access point is slow.
 *
 * Cheapest fault in a network to fix, and one of the easiest to miss. That
 * combination is what earns a permanent tile rather than a report somebody runs
 * occasionally.
 */

/** A port as the dashboard's device payload carries it. */
export interface PortLike {
  /**
   * The LIVE negotiated speed label ("1G", "100M", "10G"), or empty when the
   * port has no link. This is the field the tile keys on — NOT the faceplate's
   * `speed`, which is what the hardware can do and never changes.
   */
  link?: string | null;
  /** Operational state from the last connector read, when there is one. */
  link_state?: string | null;
}

/** One row the tile draws. */
export interface LowBandwidthPort {
  device: string;
  deviceId: string;
  port: string;
  /** The label as the device reported it, shown verbatim. */
  link: string;
  mbps: number;
}

/**
 * Speeds the tile considers LOW, in Mbit/s.
 *
 * 10 and 100 only. Deliberately NOT "anything below the port's own capability",
 * which sounds more general and is worse: a 10 G cage running at 1 G is usually
 * an SFP module doing exactly what it is meant to, and a rule that flagged it
 * would fill the tile with correct configuration on every uplink in the fleet.
 * A tile that is always full is a tile nobody reads.
 *
 * 10 and 100 are different: on the equipment this product manages, nothing is
 * supposed to negotiate there. Anything that does is worth a look.
 */
export const LOW_SPEEDS_MBPS = [10, 100];

/**
 * Parse a negotiated-speed label into Mbit/s.
 *
 * Returns null for anything unrecognised rather than guessing. A label this
 * cannot read is a port whose speed is UNKNOWN, and an unknown speed must not be
 * reported as low — the tile exists to point at real faults, and a false one
 * costs somebody a trip to a rack.
 */
export function speedMbps(label: string | null | undefined): number | null {
  if (!label) return null;
  const s = String(label).trim().toUpperCase().replace(/BPS$/, "");
  const m = s.match(/^(\d+(?:\.\d+)?)\s*([GM])$/);
  if (!m) return null;
  const n = Number(m[1]);
  if (!Number.isFinite(n) || n <= 0) return null;
  return m[2] === "G" ? n * 1000 : n;
}

/**
 * Collect every port that is UP and negotiated at a low speed.
 *
 * "Up" is taken from `link` being present, NOT from `link_state`. The two differ
 * and the difference matters here: `link` is the live speed reading, so a port
 * reporting one carries a link right now, whereas `link_state` comes from the
 * last connector read and can be minutes stale on a device nobody has re-read. A
 * tile listing a stale state would send somebody to a cable already replaced.
 *
 * A port the switch shut ITSELF is excluded when that is known: err-disabled is
 * a fault the alert tile already owns, and repeating it here would spread one
 * problem across two tiles that then disagree about how many there are.
 */
export function lowBandwidthPorts(
  devices: {
    id: string;
    name: string;
    ports?: Record<string, PortLike> | null;
  }[],
): LowBandwidthPort[] {
  const out: LowBandwidthPort[] = [];
  for (const d of devices ?? []) {
    const ports = d.ports ?? {};
    for (const name of Object.keys(ports)) {
      const p = ports[name] ?? {};
      if (!p.link) continue;
      const state = (p.link_state ?? "").toLowerCase();
      if (state === "disabled" || state === "err-disabled") continue;
      const mbps = speedMbps(p.link);
      if (mbps === null || !LOW_SPEEDS_MBPS.includes(mbps)) continue;
      out.push({
        device: d.name,
        deviceId: d.id,
        port: name,
        link: String(p.link),
        mbps,
      });
    }
  }
  // Slowest first, then device, then port. The slowest is the likeliest to be a
  // fault rather than a choice; a stable order after that keeps the list from
  // reshuffling on every refresh, which is worse than being unsorted when
  // somebody is reading down it.
  out.sort(
    (a, b) =>
      a.mbps - b.mbps ||
      a.device.localeCompare(b.device) ||
      a.port.localeCompare(b.port),
  );
  return out;
}

/**
 * A row of the site's client ledger, as much of it as this file reads.
 *
 * Declared here rather than imported from `api.ts` so the module keeps the
 * no-imports property the header explains — `ClientRow` is structurally
 * assignable to it, which is all the call site needs.
 */
export interface ClientLike {
  mac: string;
  /** What the machine announced over LLDP. Absent on most MACs. */
  name?: string;
  /** IEEE OUI registrant. Absent when the registry does not know the prefix. */
  vendor?: string;
  device_id: string;
  port: string;
  /** The last sighting falls inside the ledger's freshness window. */
  online: boolean;
  /** Other (device, port) pairs also sighting this MAC: the location hedge. */
  elsewhere?: number;
  last_seen: string;
}

/**
 * The machines CURRENTLY seen behind one port, most recent sighting first.
 *
 * Two judgements, and both are about not overstating what a forwarding table
 * proves — which is why they are asserted in `lowBandwidth.test.ts` rather
 * than left to be read out of a hover tip.
 *
 * ONLY `online` ROWS. The client ledger is append-only by design, so a MAC
 * that passed through this port once in June is still in it; listing it under
 * "behind this port" would answer a question nobody asked and send somebody to
 * look for a machine that left months ago. `online` means a bridge saw it
 * inside the ledger's freshness window, which is the only reading that makes
 * the phrase true. The whole history stays on the Clients page, which is what
 * that page is for.
 *
 * SORTED BY LAST SIGHTING, descending — not by MAC, and not by name. A caller
 * showing fewer rows than there are has to drop the least relevant ones, and
 * "least recently seen" is the only ordering of that list whose cut an
 * operator can predict. The MAC breaks ties so two sightings recorded in the
 * same second cannot reshuffle between two refreshes.
 */
export function clientsBehindPort(
  clients: ClientLike[] | undefined,
  deviceId: string,
  port: string,
): ClientLike[] {
  return (clients ?? [])
    .filter((c) => c.online && c.device_id === deviceId && c.port === port)
    .sort(
      (a, b) =>
        b.last_seen.localeCompare(a.last_seen) || a.mac.localeCompare(b.mac),
    );
}
