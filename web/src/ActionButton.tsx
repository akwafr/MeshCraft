import type { ButtonHTMLAttributes, ReactNode } from "react";
import { Link, type LinkProps } from "react-router-dom";

/**
 * Leading icons on action buttons/links.
 *
 * Every control that performs an action (create, add, save, delete, apply,
 * adopt, …) must show a small SVG before its label. Use ActionButton /
 * ActionLink — not a bare <button> with only text.
 *
 * Icon-only controls (chevron, trash without label) keep className "icon-btn"
 * and do not need this wrapper.
 */

export type ActionKind =
  | "add"
  | "create"
  | "delete"
  | "edit"
  | "rename"
  | "save"
  | "cancel"
  | "close"
  | "confirm"
  | "apply"
  | "import"
  | "export"
  | "download"
  | "copy"
  | "scan"
  | "migrate"
  | "ban"
  | "check"
  | "sites"
  | "adopt"
  | "connect"
  | "link"
  | "finish"
  | "play"
  | "pause"
  | "terminal"
  | "upload"
  | "refresh"
  | "role"
  // "more" reveals rows a list is holding back. A chevron DOWN and not a
  // plus: nothing is being created, the rest was already there.
  | "more";

type IconProps = { size?: number };

function Svg({ size = 14, children }: IconProps & { children: ReactNode }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      {children}
    </svg>
  );
}

export function ActionIcon({ kind, size = 14 }: { kind: ActionKind; size?: number }) {
  switch (kind) {
    case "add":
    case "create":
      return (
        <Svg size={size}>
          <path d="M12 5v14" />
          <path d="M5 12h14" />
        </Svg>
      );
    case "delete":
      return (
        <Svg size={size}>
          <path d="M3 6h18" />
          <path d="M8 6V4h8v2" />
          <path d="M19 6l-1 14H6L5 6" />
        </Svg>
      );
    case "edit":
    case "rename":
      return (
        <Svg size={size}>
          <path d="M12 20h9" />
          <path d="M16.5 3.5a2.1 2.1 0 0 1 3 3L7 19l-4 1 1-4 12.5-12.5z" />
        </Svg>
      );
    case "save":
      return (
        <Svg size={size}>
          <path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z" />
          <path d="M17 21v-8H7v8" />
          <path d="M7 3v5h8" />
        </Svg>
      );
    case "cancel":
      return (
        <Svg size={size}>
          <circle cx="12" cy="12" r="10" />
          <path d="M15 9l-6 6" />
          <path d="M9 9l6 6" />
        </Svg>
      );
    case "close":
      return (
        <Svg size={size}>
          <path d="M18 6L6 18" />
          <path d="M6 6l12 12" />
        </Svg>
      );
    case "confirm":
    case "check":
    case "finish":
    case "apply":
      return (
        <Svg size={size}>
          <path d="M20 6L9 17l-5-5" />
        </Svg>
      );
    case "import":
    case "download":
      return (
        <Svg size={size}>
          <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
          <path d="M7 10l5 5 5-5" />
          <path d="M12 15V3" />
        </Svg>
      );
    case "export":
    case "upload":
      return (
        <Svg size={size}>
          <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" />
          <path d="M17 8l-5-5-5 5" />
          <path d="M12 3v12" />
        </Svg>
      );
    case "copy":
      return (
        <Svg size={size}>
          <rect x="9" y="9" width="13" height="13" rx="2" />
          <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
        </Svg>
      );
    case "scan":
      return (
        <Svg size={size}>
          <circle cx="11" cy="11" r="8" />
          <path d="M21 21l-4.3-4.3" />
        </Svg>
      );
    case "migrate":
      return (
        <Svg size={size}>
          <path d="M5 12h14" />
          <path d="M12 5l7 7-7 7" />
        </Svg>
      );
    case "ban":
      return (
        <Svg size={size}>
          <circle cx="12" cy="12" r="10" />
          <path d="M4.9 4.9l14.2 14.2" />
        </Svg>
      );
    case "sites":
      return (
        <Svg size={size}>
          <path d="M3 21h18" />
          <path d="M5 21V7l7-4 7 4v14" />
          <path d="M9 21v-6h6v6" />
        </Svg>
      );
    case "adopt":
    case "role":
      return (
        <Svg size={size}>
          <path d="M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2" />
          <circle cx="9" cy="7" r="4" />
          <path d="M19 8v6" />
          <path d="M22 11h-6" />
        </Svg>
      );
    case "connect":
      return (
        <Svg size={size}>
          <path d="M12 2v6" />
          <path d="M12 16v6" />
          <path d="M8 8h8v4a4 4 0 0 1-8 0V8z" />
          <path d="M8 16h8v-2a4 4 0 0 0-8 0v2z" />
        </Svg>
      );
    case "link":
      return (
        <Svg size={size}>
          <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
          <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
        </Svg>
      );
    case "play":
      return (
        <Svg size={size}>
          <path d="M5 3l14 9-14 9V3z" />
        </Svg>
      );
    case "pause":
      return (
        <Svg size={size}>
          <path d="M8 4v16" />
          <path d="M16 4v16" />
        </Svg>
      );
    case "terminal":
      return (
        <Svg size={size}>
          <rect x="2" y="4" width="20" height="16" rx="2" />
          <path d="M6 9l4 3-4 3" />
          <path d="M12 15h6" />
        </Svg>
      );
    case "refresh":
      return (
        <Svg size={size}>
          <path d="M21 2v6h-6" />
          <path d="M3 12a9 9 0 0 1 15-6.7L21 8" />
          <path d="M3 22v-6h6" />
          <path d="M21 12a9 9 0 0 1-15 6.7L3 16" />
        </Svg>
      );
    case "more":
      return (
        <Svg size={size}>
          <path d="M6 9l6 6 6-6" />
        </Svg>
      );
  }
}

export function ActionButton({
  kind,
  children,
  className = "",
  type = "button",
  ...rest
}: {
  kind: ActionKind;
  children: ReactNode;
} & ButtonHTMLAttributes<HTMLButtonElement>) {
  const cls = className.includes("action-btn") ? className : `${className} action-btn`.trim();
  return (
    <button type={type} className={cls} {...rest}>
      <ActionIcon kind={kind} />
      <span>{children}</span>
    </button>
  );
}

export function ActionLink({
  kind,
  children,
  className = "",
  ...rest
}: {
  kind: ActionKind;
  children: ReactNode;
} & LinkProps) {
  const cls = className.includes("action-btn") ? className : `${className} action-btn`.trim();
  return (
    <Link className={cls} {...rest}>
      <ActionIcon kind={kind} />
      <span>{children}</span>
    </Link>
  );
}
