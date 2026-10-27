// The day-to-day destinations, as glyphs — UI-GUIDE §9.4. Five of them are
// the switching bar, four are the routing bar (ADR-0013); only one bar is
// ever on screen, so the phone still shows five.
//
// They exist for the mobile tab bar, where there is room for a word only if
// the word is short and the icon carries the recognition. Drawn on the same
// 24px grid with the same 1.75 stroke as the rest of the interface's inline
// SVG, so they sit in a row with the gear and the sign-out arrow without one
// of them looking bolder than the others.
//
// Two of them are deliberately LITERAL rather than conventional: "ports" is
// a patch panel and not a plug, "equipment" is a stacked rack unit and not a
// generic server tower, because the people using this know what those look
// like and the convention would be a worse clue than the thing itself.

type Props = { className?: string };

const common = {
  viewBox: "0 0 24 24",
  fill: "none",
  stroke: "currentColor",
  strokeWidth: 1.75,
  strokeLinecap: "round" as const,
  strokeLinejoin: "round" as const,
  "aria-hidden": true,
};

export function DashboardIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <rect x="3" y="3" width="7.5" height="7.5" rx="1.5" />
      <rect x="13.5" y="3" width="7.5" height="4.5" rx="1.5" />
      <rect x="13.5" y="10.5" width="7.5" height="10.5" rx="1.5" />
      <rect x="3" y="13.5" width="7.5" height="7.5" rx="1.5" />
    </svg>
  );
}

/** A two-unit stack with a status LED — a rack, seen from the front. */
export function EquipmentIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <rect x="2.5" y="4" width="19" height="6.5" rx="1.5" />
      <rect x="2.5" y="13.5" width="19" height="6.5" rx="1.5" />
      <path d="M6 7.25h.01M6 16.75h.01" strokeWidth="2.5" />
      <path d="M9.5 7.25H18M9.5 16.75H18" opacity=".45" />
    </svg>
  );
}

/** A patch panel: two interleaved rows, the way a faceplate is numbered. */
export function PortsIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <rect x="2.5" y="5" width="19" height="14" rx="2" />
      <path d="M6 9v2.5M9.5 9v2.5M13 9v2.5M16.5 9v2.5" strokeWidth="2" />
      <path d="M6 14.5V17M9.5 14.5V17M13 14.5V17M16.5 14.5V17" strokeWidth="2" opacity=".5" />
    </svg>
  );
}

/** A core with three branches — the shape the weathermap draws. */
export function TopologyIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <circle cx="12" cy="4.75" r="2.25" />
      <circle cx="4.75" cy="18.5" r="2.25" />
      <circle cx="19.25" cy="18.5" r="2.25" />
      <circle cx="12" cy="18.5" r="2.25" />
      <path d="M12 7v3.5M12 10.5H4.75v5.75M12 10.5h7.25v5.75M12 10.5v5.75" />
    </svg>
  );
}

/** What is plugged in: a screen and a handset, not people. */
export function ClientsIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <rect x="2.5" y="4.5" width="12" height="9" rx="1.5" />
      <path d="M6.5 17.5h4M8.5 13.5v4" />
      <rect x="16.5" y="9.5" width="5" height="10" rx="1.5" />
      <path d="M18.6 17.2h.8" />
    </svg>
  );
}

/* The routing bar — ADR-0013. Same grid, same stroke, same preference for
   the literal thing over the convention: a router is a fork in the road, a
   firewall is a wall. */

/** A fork: one path arriving, two leaving — what a routing table decides. */
export function RoutingIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <path d="M12 21v-6" />
      <path d="M12 15 5.5 9.5V5.5" />
      <path d="M12 15l6.5-5.5V5.5" />
      <path d="M5.5 3.5v2M18.5 3.5v2" strokeWidth="2.5" />
    </svg>
  );
}

/** A wall, coursed like brickwork. Not a shield: a shield is security, this
    is the thing that stands between two zones. */
export function FirewallIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <rect x="2.5" y="5" width="19" height="14" rx="1.5" />
      <path d="M2.5 9.7h19M2.5 14.3h19" />
      <path d="M9 5v4.7M15 5v4.7M12 9.7v4.6M6 14.3V19M18 14.3V19" opacity=".6" />
    </svg>
  );
}

/** A globe with its equator and one meridian: the name space, not a server. */
export function DnsIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <circle cx="12" cy="12" r="9" />
      <path d="M3 12h18" />
      <path d="M12 3c2.6 2.4 4 5.6 4 9s-1.4 6.6-4 9c-2.6-2.4-4-5.6-4-9s1.4-6.6 4-9z" />
    </svg>
  );
}

/** A pool handing an address out: a stack of leases and an arrow leaving. */
export function DhcpIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <rect x="2.5" y="4" width="12" height="16" rx="1.5" />
      <path d="M5.5 8h6M5.5 12h6M5.5 16h3.5" opacity=".65" />
      <path d="M15.5 12h6M18.5 9l3 3-3 3" />
    </svg>
  );
}

/** Radiation leaving a point: the arcs of a broadcast, not a signal-strength
    staircase — a bar chart would promise a quality this screen does not
    measure (ADR-0016 §5 keeps the traffic ramp away from radio). */
export function WifiIcon({ className }: Props) {
  return (
    <svg {...common} className={className}>
      <path d="M12 18.5h.01" strokeWidth="2.5" />
      <path d="M8.1 14.9a5.5 5.5 0 0 1 7.8 0" />
      <path d="M5 11.4a10.5 10.5 0 0 1 14 0" />
      <path d="M2.2 8a15.5 15.5 0 0 1 19.6 0" opacity=".6" />
    </svg>
  );
}

export const NAV_ICONS: Record<string, (p: Props) => JSX.Element> = {
  dashboard: DashboardIcon,
  equipment: EquipmentIcon,
  ports: PortsIcon,
  topology: TopologyIcon,
  clients: ClientsIcon,
  wifi: WifiIcon,
  routing: RoutingIcon,
  firewall: FirewallIcon,
  dns: DnsIcon,
  dhcp: DhcpIcon,
};
