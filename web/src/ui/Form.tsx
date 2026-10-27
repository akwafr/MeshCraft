// The Enter contract — UI-GUIDE §8.2, finally native.
//
// Two <form> elements existed in the whole app; everywhere else Enter was
// faked four incompatible ways or absent ("you must click Save"). This
// primitive renders a REAL form, so Enter-submits is the browser's own
// behaviour: no per-input onKeyDown, no idioms. Open pickers already consume
// Enter before it bubbles here; textareas keep Enter for newlines and get
// Ctrl+Enter (or Cmd+Enter) as submit.

import { useRef } from "react";

export function Form({
  onSubmit,
  busy,
  className,
  children,
}: {
  /** The primary action. The footer's primary button must be type="submit". */
  onSubmit: () => void;
  /** A write in flight: submission is refused, not queued. */
  busy?: boolean;
  className?: string;
  children: React.ReactNode;
}) {
  const ref = useRef<HTMLFormElement>(null);
  return (
    <form
      ref={ref}
      className={className}
      onSubmit={(e) => {
        e.preventDefault();
        if (!busy) onSubmit();
      }}
      onKeyDown={(e) => {
        // Ctrl+Enter submits from a textarea, where plain Enter is a newline.
        if (
          e.key === "Enter" &&
          (e.ctrlKey || e.metaKey) &&
          e.target instanceof HTMLTextAreaElement
        ) {
          e.preventDefault();
          ref.current?.requestSubmit();
        }
      }}
    >
      {children}
    </form>
  );
}
