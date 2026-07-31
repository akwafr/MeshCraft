/**
 * meshcraft brand mark: a hexagon (the mesh, the honeycomb) containing the
 * smallest possible mesh — three nodes all connected to each other, so two
 * paths between any two nodes. That is exactly what the product does:
 * devices reachable by multiple paths.
 *
 * Deliberately bold design (few elements, thick strokes): the same drawing
 * must stay readable at 16 px in a tab. The file `web/public/favicon.svg`
 * reproduces this drawing exactly — any change here must be mirrored there.
 */
export default function Logo({ size = 26 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 32 32"
      role="img"
      aria-label="meshcraft"
    >
      <title>meshcraft</title>
      <defs>
        <linearGradient id="mc-hex" x1="0" y1="0" x2="1" y2="1">
          <stop offset="0" stopColor="#6890ff" />
          <stop offset="1" stopColor="#3d63e8" />
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
          solid triangle (verified at render time). */}
      <g stroke="#eef3ff" strokeWidth="1.45" strokeLinecap="round">
        <path d="M16 9.8 L10.11 20" />
        <path d="M16 9.8 L21.89 20" />
        <path d="M10.11 20 L21.89 20" />
      </g>
      <g fill="#eef3ff">
        <circle cx="16" cy="9.8" r="3.1" />
        <circle cx="10.11" cy="20" r="3.1" />
        <circle cx="21.89" cy="20" r="3.1" />
      </g>
    </svg>
  );
}
