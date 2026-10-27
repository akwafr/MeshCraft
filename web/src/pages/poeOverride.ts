/**
 * PoE override rows for a multi-port selection: which selected ports FORCE a
 * PoE value that differs from what their OWN group (the port profile they
 * follow) prescribes.
 *
 * The single-port panel does this comparison inline (SwitchDetail's
 * `poeOverride`); this is its multi-port sibling, and the one difference is
 * the whole point: a selection may MIX groups, so each port is compared
 * against the group IT follows — never against a group common to the
 * selection, which may not exist.
 *
 * Two silences are deliberate:
 *   - a port that follows no group, or whose group was deleted (dangling
 *     reference), has nothing to compare against and yields no row;
 *   - capability filtering is the CALLER's job — a port that cannot source
 *     power holds poe:false by hardware, not by operator choice, and a row
 *     for it would read as a fixable override where no fix exists.
 *
 * Dependency-free on purpose: the structural types below are the few fields
 * the comparison needs, so `node --experimental-strip-types` can run the
 * test file against this module directly.
 */

/** The few port-intent fields the comparison reads. */
export interface PoeFollowPort {
  name: string;
  poe?: boolean;
  profile_id?: string | null;
}

/** A port profile reduced to what PoE comparison needs. */
export interface PoeFollowGroup {
  id: string;
  name: string;
  poe: boolean;
}

/** One overridden port: both values, and WHICH group it was compared to. */
export interface PoeOverrideRow {
  port: string;
  portPoE: boolean;
  groupId: string;
  groupName: string;
  groupPoE: boolean;
}

export function poeOverrides(
  ports: PoeFollowPort[],
  groups: PoeFollowGroup[],
): PoeOverrideRow[] {
  const byId = new Map(groups.map((g) => [g.id, g]));
  const out: PoeOverrideRow[] = [];
  for (const p of ports) {
    if (!p.profile_id) continue;
    const g = byId.get(p.profile_id);
    if (!g) continue;
    // Profiles write the intent on BOTH sides (poe true and false), so a
    // plain boolean inequality is the honest comparison — same rule as the
    // single-port panel.
    if (!!p.poe === !!g.poe) continue;
    out.push({
      port: p.name,
      portPoE: !!p.poe,
      groupId: g.id,
      groupName: g.name,
      groupPoE: !!g.poe,
    });
  }
  return out;
}
