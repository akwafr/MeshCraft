// The horizontal scroller, as a component — UI-GUIDE §9.2.
//
// `.scroll-x` alone is a class anyone can forget; this is the version that
// also SAYS it scrolls. It wires `useEdgeFade`, so the edge with more
// content behind it fades and the edge at the end of the content does not.
//
// Why that matters more than it sounds: a desktop gets a scrollbar under the
// table for free, so the affordance is permanent and honest. A touch screen
// shows no scrollbar until you are already scrolling, which means a table
// cut off at the right edge of a phone looks exactly like a table that ends
// there. On the Ports screen the two readings are "this switch has 8
// columns of information" and "this switch has 4", and nothing on screen
// distinguishes them.

import type { ReactNode } from "react";
import { useEdgeFade } from "./useEdgeFade";

export default function ScrollX({
  children,
  className = "",
  ...rest
}: {
  children: ReactNode;
  className?: string;
} & React.HTMLAttributes<HTMLDivElement>) {
  const fade = useEdgeFade<HTMLDivElement>();
  return (
    <div
      ref={fade.ref}
      className={"scroll-x scroll-x--faded " + className}
      {...rest}
    >
      {children}
    </div>
  );
}
