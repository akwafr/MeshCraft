/**
 * The two maps the faceplate is drawn from, out of React so they can be
 * asserted directly (facade.test.ts).
 *
 * Both are shape decisions an operator would notice getting wrong — a block
 * split in two where the hardware has one row, a port blinking that is not
 * being written — and neither needs a rendered component to check.
 */
import type { Device, PortGroup } from "../../api";
import type { PortRef } from "../../network";

/** One labelled block of the faceplate, already split into its physical rows. */
export interface FacadeBlock {
  label: string;
  rows: PortRef[][];
}

/**
 * Groups the facade by physical block; ≥16 ports → 2 rows interleaved
 * (odd above / even below) as on real hardware.
 *
 * Server-side groups sharing an explicit `row` (cf. PortGroup.row) are
 * merged into ONE block first: that field means "same physical row on the
 * faceplate, split only to carry metadata that differs within it" — a
 * connector that never sets it (every platform before this field existed,
 * and any platform whose groups ARE separate hardware blocks) keeps
 * exactly today's one-group-one-block behavior.
 */
export function buildFacade(
  groups: PortGroup[] | undefined,
  ports: PortRef[],
): FacadeBlock[] {
  const facade: FacadeBlock[] = [];
  let idx = 0;
  const blocks: { label: string; ports: PortRef[] }[] = [];
  const byRow = new Map<number, { label: string; ports: PortRef[] }>();
  for (const g of groups ?? []) {
    const slice = ports.slice(idx, idx + g.count);
    idx += g.count;
    if (g.row != null) {
      let entry = byRow.get(g.row);
      if (!entry) {
        entry = { label: g.label ?? g.prefix, ports: [] };
        byRow.set(g.row, entry);
        blocks.push(entry);
      }
      entry.ports.push(...slice);
    } else {
      blocks.push({ label: g.label ?? g.prefix, ports: slice });
    }
  }
  for (const b of blocks) {
    let rows: PortRef[][];
    if (b.ports.length >= 16) {
      const a: PortRef[] = [], c: PortRef[] = [];
      b.ports.forEach((p, i) => (i % 2 ? c : a).push(p));
      rows = [a, c];
    } else rows = [b.ports];
    facade.push({ label: b.label, rows });
  }
  return facade;
}

/**
 * The ports ACTUALLY affected by a drift, and nothing else. Flashing the
 * whole faceplate for the wrong port would send you searching for a
 * long time to find what's actually wrong.
 *
 * While an apply is actually running, the ports being written blink as
 * "provisioning" — a stronger, distinct animation from a still pending. The
 * ports in flight are exactly those the model diverges on (dev.drift).
 *
 * NOTE: a port action in flight also blinks, and that entry is added by the
 * page — right after the mutations that know about it are declared. See the
 * comment there for why it cannot come through the pipeline here.
 */
export function driftMap(
  dev: Device | undefined,
): Map<string, "pending" | "drift" | "applying"> {
  const driftPorts = new Map<string, "pending" | "drift" | "applying">();
  if (dev?.sync === "pending" || dev?.sync === "drift") {
    for (const c of dev.drift ?? []) driftPorts.set(c.port, dev.sync);
  }
  if (dev?.applying) {
    for (const c of dev.drift ?? []) driftPorts.set(c.port, "applying");
  }
  return driftPorts;
}
