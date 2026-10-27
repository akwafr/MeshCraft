// The banner — ONE layout behind every state banner in this UI.
//
// Seven components (drift, skipped, incomplete apply, failed apply, refused,
// unsupported, connector notes) plus three hand-rolled sites all rendered the
// same three-part shape — title, explanatory note, list of sentences, and
// sometimes a column of actions — each with its own copy of the markup. The
// shape lives here; the VARIANTS keep their names, their props and their
// rationale comments, because what differs between them is the operational
// meaning (which tone, which gestures, which sentences), not the layout.
//
// Two tones, and only two — UI-GUIDE §2:
//
//   act  — the banner calls for a DECISION (drift, refusal with a way out,
//          failure). Orange family, actions on the right.
//   info — nothing abnormal is happening and there is no gesture to offer
//          (platform limits, connector notes). Painting these orange would
//          teach people to ignore orange.

import type { ReactNode } from "react";

export function Banner({
  tone,
  title,
  note,
  items,
  actions,
  className,
  children,
}: {
  tone: "act" | "info";
  title: ReactNode;
  /** The explanation under the title — why this is being said here. */
  note?: ReactNode;
  /** The sentences: ports, changes, connector notes — displayed verbatim. */
  items?: ReactNode[];
  /** Right-hand action column; the variant owns which gestures exist. */
  actions?: ReactNode;
  className?: string;
  /** Extra text-column content (a second note, a caveat) — rendered after
   *  note and items; consecutive `.note`s space themselves (styles.css). */
  children?: ReactNode;
}) {
  return (
    <div
      className={(tone === "info" ? "info-banner" : "drift-banner") + (className ? " " + className : "")}
    >
      <div className="drift-text">
        <div className={tone === "info" ? "info-title" : "drift-title"}>{title}</div>
        {note ? <div className="note">{note}</div> : null}
        {items && items.length > 0 ? (
          <ul>
            {items.map((it, i) => (
              <li key={i}>{it}</li>
            ))}
          </ul>
        ) : null}
        {children}
      </div>
      {actions ? <div className="row banner-actions">{actions}</div> : null}
    </div>
  );
}
