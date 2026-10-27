/**
 * meshcraft brand mark: a hexagon (the mesh, the honeycomb) containing the
 * smallest possible mesh — three nodes all connected to each other, so two
 * paths between any two nodes. That is exactly what the product does:
 * devices reachable by multiple paths.
 *
 * Deliberately bold design (few elements, thick strokes): the same drawing
 * must stay readable at 16 px in a tab. The file `web/public/favicon.svg`
 * reproduces this drawing exactly — any change here must be mirrored there.
 *
 * Motion lives in `styles.css` (section LOGO), never in an inline style, and
 * only in two moments: the mesh converges once as the shell mounts (nodes
 * appear, then the links are traced between them), and a packet runs the
 * circuit while the pointer is on the mark. At rest nothing moves: this sits
 * in the corner of a screen somebody watches for hours. The classes below are
 * the only hooks that CSS needs; `prefers-reduced-motion: reduce` cancels
 * every animation and the markup at rest already IS the finished drawing, so
 * the frozen state is the correct one.
 */
export default function Logo({ size = 26 }: { size?: number }) {
  return (
    <svg
      className="mc-logo"
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label="meshcraft"
    >
      <title>meshcraft</title>
      <defs>
        {/* THE TWO STOPS ARE INDIRECTED THROUGH CUSTOM PROPERTIES so the
            mark can take the navigation mode's colour (ADR-0013): the top bar
            sets --logo-from / --logo-to on the mark's container in routing
            mode, and nothing else in the product may. The fallbacks ARE the
            brand blue, so every other Logo — the login screen's, a browser
            with no custom-property support at all — draws exactly what it
            drew before, and web/public/favicon.svg keeps the literals. */}
        <linearGradient id="mc-hex" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="var(--logo-from, #6890ff)" />
          <stop offset="1" stopColor="var(--logo-to, #3d63e8)" />
        </linearGradient>
      </defs>
      {/* Hexagon: stroke drawn over the fill to round the corners without
          computing arcs. */}
      <path
        d="M16 2.8 L27.4 9.4 L27.4 22.6 L16 29.2 L4.6 22.6 L4.6 9.4 Z"
        fill="url(#mc-hex)"
        stroke="url(#mc-hex)"
        strokeWidth="3.2"
        strokeLinejoin="round"
      />
      {/* The mesh: three links, three nodes. Bold nodes and thin links —
          at 16 px the nodes must stay distinct instead of merging into a
          solid triangle. Each node carries a ring of the hexagon's own
          gradient, which cuts the link just before it reaches the node: that
          gap is what keeps three dots readable once the favicon is
          rasterised at 16 px (checked by rendering it, not by eye at 100 %).
          Each link is 11.78 units long — the dash length the tracing
          animation uses. */}
      <g stroke="#eef3ff" strokeWidth="1.15" strokeLinecap="round">
        <path className="mc-link" d="M16 9.8 L10.11 20" />
        <path className="mc-link" d="M16 9.8 L21.89 20" />
        <path className="mc-link" d="M10.11 20 L21.89 20" />
      </g>
      {/* The packet: the three links again as one closed circuit (perimeter
          35.34), carrying a single short, thicker dash. It is invisible at
          rest and only moves on hover. Thickness, not colour, is what makes
          it legible — so it reads the same whatever the page theme, and it
          passes BEHIND the nodes below, which gives the hop its rhythm. */}
      <path
        className="mc-packet"
        d="M16 9.8 L21.89 20 L10.11 20 Z"
        fill="none"
        stroke="#ffffff"
        strokeWidth="3"
        strokeLinecap="round"
      />
      <g fill="#eef3ff" stroke="url(#mc-hex)" strokeWidth="1.4">
        <circle className="mc-node" cx="16" cy="9.8" r="3.2" />
        <circle className="mc-node" cx="10.11" cy="20" r="3.2" />
        <circle className="mc-node" cx="21.89" cy="20" r="3.2" />
      </g>
    </svg>
  );
}
