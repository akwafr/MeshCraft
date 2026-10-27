import { useEffect, useRef } from "react";
import {
  HALO_RADIUS,
  RAIN_GLYPHS,
  linkAlpha,
  nearestWithin,
  newDrop,
  stepDrop,
  withAlpha,
  type Drop,
  type Point,
} from "./loginRain";

// One glyph per CELL × CELL square, and the fall advances one tick every
// TICK_MS — slower than the frame rate on purpose: a rain redrawn at 60 Hz
// is a blur, and the pointer layer, which does follow every frame, is the
// part that has to feel alive.
const CELL = 16;
const TICK_MS = 55;
// How much of the previous tick survives: the trail's length. Alone it never
// finishes the job: 8 % of a difference under 7 levels rounds to nothing, so
// a ghost of every glyph stays for good — invisible on the dark ground,
// a grey field on the light one. Every SWEEP_EVERY ticks a stronger pass
// takes the residue down to one level.
const FADE = 0.08;
const SWEEP = 0.4;
const SWEEP_EVERY = 8;
// The rain is a backdrop, not the subject; the lit heads are at full alpha.
const DIM = 0.5;
const STILL_TRAIL = 14;

interface Colors {
  bg: string;
  accent: string;
  text: string;
}

// The tokens are `light-dark()` pairs, which getPropertyValue returns
// unresolved; a probe's COMPUTED colour is the resolved one, in the theme
// actually in force.
function readColors(): Colors {
  const probe = document.createElement("span");
  document.body.appendChild(probe);
  const read = (token: string) => {
    probe.style.color = `var(${token})`;
    return getComputedStyle(probe).color;
  };
  const colors = { bg: read("--bg"), accent: read("--accent"), text: read("--text") };
  probe.remove();
  return colors;
}

/**
 * The login screen's falling-glyph backdrop, with a halo that follows the
 * pointer and wires it to the nearest heads. Two canvases: the rain keeps
 * its trail by painting over itself, so the pointer layer — cleared every
 * frame — cannot live on it without smearing.
 *
 * Under `prefers-reduced-motion` the rain is painted once and stays still;
 * on a coarse pointer there is no pointer to follow. Decorative only, so
 * the whole thing is `aria-hidden` (UI-GUIDE §4).
 */
export default function LoginBackdrop() {
  const rainRef = useRef<HTMLCanvasElement>(null);
  const glowRef = useRef<HTMLCanvasElement>(null);

  useEffect(() => {
    const rain = rainRef.current;
    const glow = glowRef.current;
    const rc = rain?.getContext("2d");
    const gc = glow?.getContext("2d");
    if (!rain || !glow || !rc || !gc) return;

    const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const follows = !still && !window.matchMedia("(pointer: coarse)").matches;
    const dpr = Math.min(window.devicePixelRatio || 1, 2);

    let colors = readColors();
    let width = 0;
    let height = 0;
    let rows = 0;
    let drops: Drop[] = [];
    const drawnRow: number[] = [];
    // Heads on screen at the last tick: what the pointer wires itself to.
    let heads: Point[] = [];
    const pointer = { x: 0, y: 0, on: false };
    let glowDirty = false;

    const glyph = () => RAIN_GLYPHS[Math.floor(Math.random() * RAIN_GLYPHS.length)];

    const paintBackground = () => {
      rc.globalAlpha = 1;
      rc.fillStyle = colors.bg;
      rc.fillRect(0, 0, width, height);
    };

    // Reduced motion: one frame, each column frozen mid-fall with its trail.
    const paintStill = () => {
      paintBackground();
      rc.fillStyle = colors.accent;
      for (let i = 0; i < drops.length; i++) {
        const head = Math.floor(Math.random() * rows);
        for (let k = 0; k < STILL_TRAIL && head - k >= 0; k++) {
          rc.globalAlpha = DIM * (1 - k / STILL_TRAIL);
          rc.fillText(glyph(), i * CELL + 1, (head - k) * CELL);
        }
      }
    };

    const resize = () => {
      width = rain.clientWidth;
      height = rain.clientHeight;
      for (const c of [rain, glow]) {
        c.width = Math.round(width * dpr);
        c.height = Math.round(height * dpr);
      }
      // Resizing a canvas resets its context, font included.
      rc.setTransform(dpr, 0, 0, dpr, 0, 0);
      gc.setTransform(dpr, 0, 0, dpr, 0, 0);
      rc.font = `${CELL - 2}px ui-monospace, SFMono-Regular, Menlo, Consolas, monospace`;
      rc.textBaseline = "top";
      rows = Math.ceil(height / CELL);
      const cols = Math.ceil(width / CELL);
      drops = Array.from({ length: cols }, (_, i) => drops[i] ?? newDrop(rows, Math.random));
      if (still) paintStill();
      else paintBackground();
    };

    let ticks = 0;
    const tick = () => {
      ticks++;
      rc.globalAlpha = ticks % SWEEP_EVERY === 0 ? SWEEP : FADE;
      rc.fillStyle = colors.bg;
      rc.fillRect(0, 0, width, height);
      heads = [];
      for (let i = 0; i < drops.length; i++) {
        const row = Math.floor(drops[i].y);
        // A head slower than a row per tick stays in its cell for a few
        // ticks; drawing each time would stack glyphs into a smudge.
        const fresh = row !== drawnRow[i];
        drawnRow[i] = row;
        if (row >= 0 && row < rows) {
          const head = { x: i * CELL + CELL / 2, y: row * CELL + CELL / 2 };
          heads.push(head);
          if (fresh) {
            const lit =
              pointer.on && Math.hypot(head.x - pointer.x, head.y - pointer.y) < HALO_RADIUS;
            rc.globalAlpha = lit ? 1 : DIM;
            rc.fillStyle = lit ? colors.text : colors.accent;
            rc.fillText(glyph(), i * CELL + 1, row * CELL);
          }
        }
        drops[i] = stepDrop(drops[i], rows, Math.random);
      }
    };

    const paintGlow = () => {
      if (!pointer.on && !glowDirty) return;
      gc.clearRect(0, 0, width, height);
      glowDirty = pointer.on;
      if (!pointer.on) return;
      const { x, y } = pointer;
      const halo = gc.createRadialGradient(x, y, 0, x, y, HALO_RADIUS);
      halo.addColorStop(0, withAlpha(colors.accent, 0.14));
      halo.addColorStop(1, withAlpha(colors.accent, 0));
      gc.globalAlpha = 1;
      gc.fillStyle = halo;
      gc.beginPath();
      gc.arc(x, y, HALO_RADIUS, 0, Math.PI * 2);
      gc.fill();
      gc.strokeStyle = colors.accent;
      gc.fillStyle = colors.accent;
      gc.shadowColor = colors.accent;
      gc.lineWidth = 1.2;
      for (const { index, dist } of nearestWithin(heads, pointer)) {
        const h = heads[index];
        const a = linkAlpha(dist);
        gc.globalAlpha = a;
        gc.shadowBlur = 0;
        gc.beginPath();
        gc.moveTo(x, y);
        gc.lineTo(h.x, h.y);
        gc.stroke();
        gc.globalAlpha = Math.min(1, a * 2);
        gc.shadowBlur = 8;
        gc.beginPath();
        gc.arc(h.x, h.y, 2.6, 0, Math.PI * 2);
        gc.fill();
      }
      gc.shadowBlur = 0;
    };

    let raf = 0;
    let last = 0;
    const frame = (now: number) => {
      if (now - last >= TICK_MS) {
        tick();
        last = now;
      }
      if (follows) paintGlow();
      raf = requestAnimationFrame(frame);
    };

    const move = (e: MouseEvent) => {
      const r = glow.getBoundingClientRect();
      pointer.x = e.clientX - r.left;
      pointer.y = e.clientY - r.top;
      pointer.on = true;
    };
    // `relatedTarget` is null only when the pointer leaves the window.
    const leave = (e: MouseEvent) => {
      if (!e.relatedTarget) pointer.on = false;
    };

    // A theme toggle stamps an attribute on <html> (ui/theme.ts); a system
    // switch only changes the media query. Either way the colours re-resolve.
    const retheme = () => {
      colors = readColors();
      if (still) paintStill();
      else paintBackground();
    };
    const scheme = window.matchMedia("(prefers-color-scheme: dark)");
    const themeAttr = new MutationObserver(retheme);

    resize();
    window.addEventListener("resize", resize);
    scheme.addEventListener("change", retheme);
    themeAttr.observe(document.documentElement, { attributes: true });
    if (follows) {
      window.addEventListener("mousemove", move);
      document.addEventListener("mouseout", leave);
    }
    if (!still) raf = requestAnimationFrame(frame);

    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener("resize", resize);
      scheme.removeEventListener("change", retheme);
      themeAttr.disconnect();
      window.removeEventListener("mousemove", move);
      document.removeEventListener("mouseout", leave);
    };
  }, []);

  return (
    <div className="login-backdrop" aria-hidden="true">
      <canvas ref={rainRef} />
      <canvas ref={glowRef} />
    </div>
  );
}
