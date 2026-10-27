import { useState, type ReactNode } from "react";

/**
 * Collapsible block in the device side panel. Tall sections (management
 * addressing, VLAN plan) start folded so the default view stays a summary.
 */
export function SidePanelGroup({
  title,
  meta,
  defaultOpen,
  children,
}: {
  title: string;
  meta?: ReactNode;
  defaultOpen: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <details
      className="side-panel-group"
      open={open}
      onToggle={(e) => setOpen(e.currentTarget.open)}
    >
      <summary className="side-panel-group-sum">
        <span className="kicker">{title}</span>
        {meta != null && meta !== "" && (
          <span className="note side-panel-group-meta">{meta}</span>
        )}
      </summary>
      <div className="side-panel-group-body">{children}</div>
    </details>
  );
}
