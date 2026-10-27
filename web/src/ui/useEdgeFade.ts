// A horizontal scroller that SAYS it has more — UI-GUIDE §9.2.
//
// On a desktop a scrollbar appears under the content and the affordance is
// free. On a touch screen there is no scrollbar until you are already
// scrolling, so a table or a faceplate that runs past the right edge looks
// exactly like one that happens to end there. The operator concludes the
// device has 8 ports.
//
// This sets `--fade-l` / `--fade-r` on the element, which `.scroll-x--faded`
// turns into a mask: the edge that has more content off screen fades out,
// and the edge that does not stays crisp. The cue is therefore never a lie —
// it disappears when you reach the end.

import { useCallback, useEffect, useRef } from "react";

const FADE = 24; // px of fade at an edge that has more

export function useEdgeFade<T extends HTMLElement>() {
  const ref = useRef<T | null>(null);

  const update = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    // `scrollWidth - clientWidth` is the total scrollable distance; the
    // 1px slack absorbs the sub-pixel rounding that otherwise leaves a
    // permanent 0.5px fade on a scroller that is exactly full.
    const max = el.scrollWidth - el.clientWidth;
    const left = max > 1 && el.scrollLeft > 1 ? FADE : 0;
    const right = max > 1 && el.scrollLeft < max - 1 ? FADE : 0;
    el.style.setProperty("--fade-l", left + "px");
    el.style.setProperty("--fade-r", right + "px");
  }, []);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    update();
    el.addEventListener("scroll", update, { passive: true });
    // The content can change width without the element resizing (a device
    // gains ports on a reread) and the element can resize without the
    // content changing (the drawer opens). Both need a recheck.
    const ro = new ResizeObserver(update);
    ro.observe(el);
    if (el.firstElementChild) ro.observe(el.firstElementChild);
    return () => {
      el.removeEventListener("scroll", update);
      ro.disconnect();
    };
  }, [update]);

  return { ref, update };
}
