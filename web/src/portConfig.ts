/**
 * WHAT A PORT IS CONFIGURED TO CARRY, in words: the VLANs it serves and the
 * profile it follows.
 *
 * # Why this is a module and not three lines in a component
 *
 * Two overlays answer the same hover. `PortSquare` puts these lines in its own
 * `QuickTip`; the floating `PortHoverTip` puts them above its charts. On a
 * faceplate only the second is ever read — both go through `hoverExclusive`,
 * and the floating tip claims the slot a beat after the bubble opens, so the
 * bubble is dismissed before anyone can read it (verified in a browser:
 * hovering a square on the inventory leaves exactly one overlay in the DOM).
 * The bubble still serves the screens that draw squares with no floating chart
 * — the topology's mini-faceplates, the port table, the profile preview — so
 * both keep their copy, and one function phrases both. Two call sites wording
 * the same jack differently is how an operator learns that "the tooltip on the
 * inventory says something else".
 *
 * Out of React, and taking `t` rather than reaching for `i18n`, for the reason
 * `portFacts.ts` does: it makes every branch below assertable by a bare node
 * process (portConfig.test.ts), and there are more branches here than a
 * component should hide.
 */
import type { PortIntent } from "./api";
// TYPE-ONLY, and that matters: `network.ts` imports i18n, which a bare node
// test cannot load. A type import is erased before the test ever runs.
import type { VlanMap } from "./network";

/**
 * The site's PORT-PROFILE vocabulary, id → name — the exact counterpart of
 * `VlanMap` and present for the same reason: a port carries a `profile_id`,
 * which is a UUID, and a square cannot name what it follows without the
 * site's list.
 */
export type ProfileNameMap = Map<string, string>;

export const profileNameMap = (
  profiles: { id: string; name: string }[] | undefined,
): ProfileNameMap => new Map((profiles ?? []).map((p) => [p.id, p.name]));

/** A trunk's tagged list is truncated past this many VLANs. */
export const TIP_TAGGED_MAX = 8;

/**
 * The configuration lines for one port, in reading order.
 *
 * # It describes the RECORDED configuration, and that is deliberate
 *
 * These lines are the intent meshcraft stores (`PortIntent`), not a reading
 * taken off the switch — the product keeps no per-port "observed VLAN" to
 * show. That is not a gap papered over here: a port whose device disagrees
 * already blinks on the faceplate, and the tip's own drift line ("the device
 * does not carry this configuration") sits immediately below these. So the tip
 * states what is WANTED and the line under it says when the box has not got
 * it, which is the same split the port panel makes.
 *
 * # Absences are stated, never guessed
 *
 * `profiles` undefined means THE SCREEN HAS NO PROFILE VOCABULARY (a catalogue
 * preview, an inventory whose query has not landed yet) and the profile line
 * is omitted — emphatically not "no profile", which is a different claim, and
 * one that would be wrong on every port for the first moments of a page load.
 * With a vocabulary in hand, a port carrying no `profile_id` is told it
 * follows none: the word "default" is never printed, because no such profile
 * exists and a port outside every group is the ordinary case, not a fallback.
 *
 * A `mode` of `libre` (or absent) produces NO VLAN line at all. The tip's
 * first line already says "free", or "active link not managed by meshcraft" on
 * a jack passing traffic anyway, and repeating that as a VLAN fact would read
 * as a VLAN the port is in.
 */
export function portConfigLines(
  p: PortIntent,
  vm: VlanMap,
  profiles: ProfileNameMap | undefined,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string[] {
  const out: string[] = [];

  if (p.mode === "access") {
    if (p.pvid == null) {
      out.push(t("netui.tipVlanAccessNone"));
    } else {
      const name = vm.get(p.pvid)?.name;
      out.push(
        name
          ? t("netui.tipVlanAccessNamed", { pvid: p.pvid, name })
          : t("netui.tipVlanAccess", { pvid: p.pvid }),
      );
    }
  } else if (p.mode === "trunk") {
    out.push(
      p.pvid == null
        ? t("netui.tipVlanTrunk")
        : t("netui.tipVlanTrunkNative", { pvid: p.pvid }),
    );
    // "all" is its own sentence rather than an expanded list: the intent
    // really is "every VLAN, including ones not created yet", and printing
    // today's numbers would freeze a membership that is deliberately open.
    if (p.tagged === "all") {
      out.push(t("netui.tipTaggedAll"));
    } else if (Array.isArray(p.tagged) && p.tagged.length > 0) {
      const ids = p.tagged.slice().sort((a, b) => a - b);
      // A 40-VLAN trunk must not turn a hover into a paragraph. The head of
      // the list plus a count stays on one line and still answers "is 20 in
      // there" for an ordinary trunk; the port panel carries the full list.
      out.push(
        ids.length > TIP_TAGGED_MAX
          ? t("netui.tipTaggedMore", {
              list: ids.slice(0, TIP_TAGGED_MAX).join(", "),
              count: ids.length - TIP_TAGGED_MAX,
            })
          : t("netui.tipTagged", { list: ids.join(", ") }),
      );
    }
  }

  if (profiles) {
    if (!p.profile_id) {
      out.push(t("netui.tipProfileNone"));
    } else {
      const name = profiles.get(p.profile_id);
      // A `profile_id` whose profile was DELETED keeps its own sentence. The
      // stamped configuration is still on the port; only the provenance is
      // gone, and the port panel says exactly that (`dangling`). Falling back
      // to "no profile" here would contradict it.
      out.push(
        name ? t("netui.tipProfile", { name }) : t("netui.tipProfileDeleted"),
      );
    }
  }

  return out;
}
