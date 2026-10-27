// The vendor badge: a brand mark with the registrant's name over it.
//
// # Where the mark comes from, and where it cannot
//
// The OUI registry answers with the ORGANISATION'S LEGAL NAME ("Routerboard.com",
// "Intel Corporate", "Samsung Electronics Co.,Ltd"), which is not a logo. The
// marks come from simple-icons (CC0), matched by substring rules curated below —
// and the match MUST be curated, because a generic slugifier would turn
// "Apple, Inc." and "Applied Materials" into the same guess. A vendor without a
// rule — Arista and HPE are real examples: simple-icons carries neither — gets a
// MONOGRAM: initial letters on a hue derived from the name, so two vendors
// side by side stay tellable apart even with no artwork for either.
//
// # Why the name is always rendered, not only the mark
//
// The request this page answers is "logo of the vendor with overlay of the
// name": a mark alone would make the reader memorise icon vocabularies, and
// the monogram fallback is meaningless without its caption. The name IS the
// datum; the mark is the recognition shortcut.
import {
  siApple,
  siAsus,
  siCisco,
  siDell,
  siEspressif,
  siGoogle,
  siHomeassistant,
  siHp,
  siHuawei,
  siIntel,
  siLenovo,
  siLg,
  siMikrotik,
  siNetgear,
  siProxmox,
  siQnap,
  siRaspberrypi,
  siSamsung,
  siShelly,
  siSony,
  siSynology,
  siTplink,
  siUbiquiti,
  siVmware,
  siXiaomi,
  type SimpleIcon,
} from "simple-icons";

/** Substring rules, checked in order against the lowercased registrant name. */
const RULES: Array<[string, SimpleIcon]> = [
  ["apple", siApple],
  ["cisco", siCisco],
  ["intel", siIntel],
  ["samsung", siSamsung],
  // MikroTik registers its OUIs as "Routerboard.com".
  ["routerboard", siMikrotik],
  ["mikrotik", siMikrotik],
  ["ubiquiti", siUbiquiti],
  ["hewlett", siHp],
  ["hp ", siHp],
  ["dell", siDell],
  ["raspberry", siRaspberrypi],
  ["proxmox", siProxmox],
  ["netgear", siNetgear],
  ["tp-link", siTplink],
  ["asus", siAsus],
  ["lenovo", siLenovo],
  ["lg ", siLg],
  ["sony", siSony],
  ["xiaomi", siXiaomi],
  ["huawei", siHuawei],
  ["google", siGoogle],
  ["espressif", siEspressif],
  ["vmware", siVmware],
  ["synology", siSynology],
  ["qnap", siQnap],
  ["shelly", siShelly],
  ["home assistant", siHomeassistant],
];

function iconFor(vendor: string): SimpleIcon | null {
  const low = vendor.toLowerCase();
  for (const [needle, icon] of RULES) {
    if (low.includes(needle)) return icon;
  }
  return null;
}

/** Deterministic hue from the name, so a vendor keeps its colour across renders. */
function hueOf(name: string): number {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
  return h;
}

/**
 * Brand colour, except when it would vanish: several marks are near-black
 * (Apple, Sony) and the page renders in the viewer's theme — on dark ground a
 * #000 glyph is not a logo, it is a hole. Below the luminance floor the glyph
 * takes the text colour instead, which is themed by construction.
 */
function fillFor(hex: string): string {
  const r = parseInt(hex.slice(0, 2), 16);
  const g = parseInt(hex.slice(2, 4), 16);
  const b = parseInt(hex.slice(4, 6), 16);
  const luma = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  return luma < 48 ? "currentColor" : "#" + hex;
}

function monogram(name: string): string {
  const words = name
    .replace(/[^\p{L}\p{N} ]/gu, " ")
    .split(/\s+/)
    .filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}

/**
 * The badge. `randomized` replaces the registry answer entirely: a
 * locally-administered address has no vendor by construction, and drawing a
 * question mark would send someone hunting a registry gap that is not one.
 */
export function VendorBadge({
  vendor,
  randomized,
  labelWhenRandomized,
  labelWhenUnknown,
}: {
  vendor?: string;
  randomized?: boolean;
  labelWhenRandomized: string;
  labelWhenUnknown: string;
}) {
  if (randomized) {
    return (
      <span className="vendor-badge" title={labelWhenRandomized}>
        <span className="vendor-mono" style={{ background: "hsl(0 0% 55%)" }}>
          ∅
        </span>
        <span className="vendor-name note">{labelWhenRandomized}</span>
      </span>
    );
  }
  if (!vendor) {
    return (
      <span className="vendor-badge">
        <span className="vendor-mono" style={{ background: "hsl(0 0% 70%)" }}>
          ?
        </span>
        <span className="vendor-name note">{labelWhenUnknown}</span>
      </span>
    );
  }
  const icon = iconFor(vendor);
  return (
    <span className="vendor-badge" title={vendor}>
      {icon ? (
        <svg
          className="vendor-logo"
          viewBox="0 0 24 24"
          width={18}
          height={18}
          aria-hidden
          fill={fillFor(icon.hex)}
        >
          <path d={icon.path} />
        </svg>
      ) : (
        <span
          className="vendor-mono"
          style={{ background: `hsl(${hueOf(vendor)} 45% 45%)` }}
        >
          {monogram(vendor)}
        </span>
      )}
      <span className="vendor-name">{vendor}</span>
    </span>
  );
}
