// The canonical page header — UI-GUIDE §8/new-screen step 1. One shell for
// the ".toolbar + h1" shape every fleet page already uses, so the divergent
// ".card-head as page title" variant stops being copied.

import type { ReactNode } from "react";

export function PageHeader({
  title,
  subtitle,
  children,
}: {
  title: ReactNode;
  /** The grey qualifier beside the title (".sub"). */
  subtitle?: ReactNode;
  /** Right side: the page's primary ActionButton(s), filters. */
  children?: ReactNode;
}) {
  return (
    <div className="toolbar">
      <h1>
        {title}
        {subtitle && <span className="sub">{subtitle}</span>}
      </h1>
      {children}
    </div>
  );
}
