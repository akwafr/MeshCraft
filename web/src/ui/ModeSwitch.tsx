/**
 * The Switching ↔ Routing selector — ADR-0013, narrowed by ADR-0019, and
 * made a plain toggle by ADR-0031.
 *
 * IT IS A SWITCH. One press anywhere on the pill changes the mode and the
 * mark slides to the other side. That is the whole control.
 *
 * WHAT IT WAS, AND WHY IT IS NOT THAT ANY MORE. The mark used to be two
 * things at once: the knob of this selector (dragged left for switching,
 * right for routing) and the way back to the dashboard (clicked). ADR-0013
 * had named that risk in its consequences — "two gestures on one object"
 * — and the departure was measured: a 24px threshold, a 1.7 axis dominance,
 * pointer capture so a finger leaving a 26px knob is still followed,
 * `draggable={false}` to kill the native link-drag, and the click after a
 * gesture swallowed once. It worked in the lab. From use: "the switcher is
 * still buggy", because the only way to change mode was a gesture nothing
 * announced, on an object whose click did something else. So:
 *
 *   - the drag is GONE, not optional. There are no pointer handlers here any
 *     more, no threshold, no `--knob-dx`, no swallowed click;
 *   - the mark no longer leads home. The "Dashboard" tab is in BOTH bars
 *     (navMode.ts, asserted by navMode.test.ts), in the top bar at 1440 and
 *     in the thumb bar at 393, and before a site exists the gear leads to
 *     configuration. The function is not lost, it stops being doubled;
 *   - the mark is therefore `aria-hidden` and out of the tab order. Its click
 *     is a POINTER SHORTCUT: everything it does, the two radios below do too.
 *
 * THERE IS NO DEAD ZONE, which is the geometric point worth keeping. The two
 * radios tile the pill, and the mark covers the ACTIVE half exactly (styles.css:
 * absolute, 50% of the track). So the only two surfaces a pointer can reach
 * are the mark (toggles) and the half you are NOT in (selects itself, which
 * is the same thing). Aiming at the logo and landing beside it does the same
 * thing as hitting it, which is the failure the old 16px blank positions had.
 *
 * WHAT ADR-0019 ESTABLISHED AND THIS KEEPS. The pill carries the mark and
 * nothing else — "Switching" / "Routing" are hover-and-focus labels
 * through `QuickTip`, never a native `title=`, which is what gave a
 * contended top bar 134px back. The accessible names did not go: this is
 * still a `radiogroup` of two `radio`s, each named, each announcing
 * `aria-checked`, each reachable by Tab and operable by Space. A screen
 * reader hears exactly what it heard before.
 *
 * AND THE HOVER LABEL NOW NAMES THE DESTINATION. The mark's tip is the mode
 * you would GO to, which happens to be the very word the reachable half
 * shows — because the reachable half is always the one you are not in. Two
 * surfaces, one word, one outcome: nothing on this control promises
 * something other than what it does.
 *
 * WHY THERE IS STILL NO CONFLICT WITH THE PAGE SWIPE (ADR-0009): that
 * recogniser binds `pointerdown` on the content element only (ui/swipe.ts),
 * and the top bar is not inside it. With the drag gone this is true for a
 * second reason as well — there is no pointer handler here at all, so the
 * browser keeps both axes and a thumb starting on the mark scrolls the page
 * like anywhere else.
 */

import { useTranslation } from "react-i18next";
import { QuickTip } from "../components";
import Logo from "../Logo";
import { otherMode, type NavMode } from "../navMode";

export default function ModeSwitch({
  mode,
  onMode,
}: {
  mode: NavMode;
  onMode: (m: NavMode) => void;
}) {
  const { t } = useTranslation();
  const label = (m: NavMode) =>
    t(m === "routing" ? "nav.modeRouting" : "nav.modeSwitching") as string;
  const next = otherMode(mode);

  /**
   * One position of the selector: a real radio with no ink of its own.
   *
   * The label lives in two places and neither of them is the view: in
   * `aria-label`, which is what a screen reader reads, and in the `QuickTip`,
   * which is what a pointer or a Tab key reveals. The button is the hover
   * surface, so it has a size — `.modesw-seg` in styles.css — and it paints
   * nothing until it is approached.
   *
   * Only the INACTIVE one can be hovered or clicked: the mark covers the
   * other. Which is why a position's tip and the mark's tip always agree.
   */
  const seg = (value: NavMode) => (
    <QuickTip text={label(value)} className={"modesw-tip modesw-tip--" + value}>
      <button
        type="button"
        role="radio"
        aria-checked={mode === value}
        aria-label={label(value)}
        className={"modesw-seg" + (mode === value ? " on" : "")}
        onClick={() => onMode(value)}
      />
    </QuickTip>
  );

  return (
    <div
      className="modesw"
      data-mode={mode}
      role="radiogroup"
      aria-label={t("nav.mode") as string}
    >
      {seg("switching")}
      {seg("routing")}
      {/* The mark, which is the knob and nothing else now. Hidden from
          assistive technology and untabbable on purpose (ADR-0031): it is a
          pointer shortcut over the two radios, not a third control, and a
          brand mark is not a name anybody needs read out — the document title
          carries "meshcraft". It keeps the `brand` class so the
          packet-on-hover animation (styles.css, LOGO) still applies. */}
      <QuickTip text={label(next)} className="modesw-knob-tip">
        <div
          className="brand modesw-knob"
          aria-hidden="true"
          onClick={() => onMode(next)}
        >
          <Logo />
        </div>
      </QuickTip>
    </div>
  );
}
