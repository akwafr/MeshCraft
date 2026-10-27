// Who is signed in — one glyph on the bar, everything else behind it.
//
// THE IDENTITY GROUP WAS THE BAR'S LARGEST OBJECT. Measured on this branch:
// 398px of a bar that wanted 1180px and had 960, which is why the bar
// overflowed at 25 of the widths between 721 and 1440px. Name, role badge,
// flag, appearance segments and sign-out were five controls answering
// questions that are asked once a session, parked permanently next to the
// five tabs and the site selector, which are asked on every click. So they
// are all one icon now, and the bar wants ~380px less.
//
// THE NAME IS STILL THE DOOR TO THE PROFILE (ADR-0011). API tokens are the
// one kind of object nobody else can see or revoke, so the way in is the
// identity itself rather than an entry in Configuration. It moved from the
// bar into the first row of this menu, where it keeps its role badge and
// gains a stated destination — and because the glyph is now on the bar at
// EVERY width, that door no longer depends on a ≤1140px fallback keeping it
// alive. The fallback is gone; what it protected is unconditional.
//
// THREE WAYS IN, AND THE REASON IS NOT SYMMETRY. "On hover" was the request;
// hover alone would have been a defect, because sign-out now lives in here
// and a finger never hovers — it would have made logging out impossible on a
// phone, and reaching it by keyboard a guess. So:
//
//   pointer  pointerenter opens, pointerleave closes — unless the menu was
//            PINNED (see below). Touch pointers are ignored here, exactly as
//            QuickTip ignores them, because a tap fires enter+leave+click.
//   click    opens pinned; on an already-hovered menu the click PINS what is
//            open rather than toggling it shut, which is what makes "hover,
//            then click to keep it" behave the way a pointer expects.
//   keyboard focus-visible on the trigger opens it pinned (a keyboard-opened
//            menu must not evaporate because the mouse is elsewhere), and
//            ↑/↓/Enter/Space open it from the trigger through usePicker.
//
// Pinned is the whole trick for the classic hover-menu defect — the pointer
// crossing the gap between the icon and the panel. Two things answer it: the
// panel is a DOM CHILD of this box, so travelling into it never leaves the
// box at all, and `.accountmenu-pop::before` bridges the 6px of air between
// them so the crossing itself is never "outside" either.
//
// usePicker owns the genuinely hard part — open state, the dismissal-stack
// registration that makes Escape close the language list BEFORE this menu
// (UI-GUIDE §8.1), outside-click, focus restoration, and closing when focus
// leaves the box (Tab past the last row). `items` is empty on purpose: the
// rows here are a link, a radio group, a picker and a button, not options of
// one list, so each keeps its own native keyboard behaviour and Tab is the
// vocabulary that moves between them.
//
// NO QuickTip ON THE TRIGGER. A tip and this menu would open on the same
// gestures and overlap, and the menu says strictly more than the tip could
// (the name, the role, where each row goes). The accessible name an icon-only
// control owes a reader who cannot see it is on `aria-label`, and it names
// the person rather than the category — "Account — Ada Byron".
//
// Lives in ui/ with the other primitives, so the bare <button>s below are
// the implementation of a control rather than a page reaching past one.

import { useRef } from "react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router-dom";
import type { Role } from "../api";
import LanguageSelector from "../i18n/LanguageSelector";
import { usePicker } from "./picker";
import ThemeControl from "./ThemeControl";

function PersonIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2" />
      <circle cx="12" cy="7" r="4" />
    </svg>
  );
}

function LogoutIcon() {
  return (
    <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <path d="M16 17l5-5-5-5" />
      <path d="M21 12H9" />
    </svg>
  );
}

// The avatar's letters: first letter of the first two words, so "Ada
// Byron" is AB and "Administrateur" is A. An e-mail used as the display
// name gives its first letter. Letters, not a photo: meshcraft has none.
function initials(name: string): string {
  const words = name.trim().split(/[\s@._-]+/).filter(Boolean);
  return words
    .slice(0, 2)
    .map((w) => w[0]!.toUpperCase())
    .join("");
}

export default function AccountMenu({
  displayName,
  isGlobalAdmin,
  siteRole,
  onLogout,
}: {
  displayName: string;
  isGlobalAdmin: boolean;
  /** The role on the site currently open, undefined when none is. The
   *  permission that answers "what may I do HERE" (ADR-46584 §3); the full
   *  list is on the profile, one click away from the first row. */
  siteRole?: Role["role"];
  onLogout: () => void;
}) {
  const { t } = useTranslation();

  // A hover-opened menu follows the pointer out again; a clicked or
  // keyboard-opened one stays until it is dismissed. A ref, not state:
  // nothing renders differently, and the pointer handlers must read the
  // value the events see rather than the one the last render saw.
  const pinned = useRef(false);

  const picker = usePicker<never>({
    items: [],
    onPick: () => {},
    onDismiss: () => {
      pinned.current = false;
    },
  });

  // Closed by the POINTER walking away: no refocus. Pulling focus back to a
  // trigger the pointer has just left is the same mistake as stealing it
  // from the field a Tab asked for (picker.ts says so for focus-out).
  const closeByPointer = () => {
    pinned.current = false;
    picker.setOpen(false);
  };

  const openPinned = () => {
    pinned.current = true;
    picker.openAt();
  };

  const label = (t("nav.account") as string) + (displayName ? " — " + displayName : "");

  return (
    <div
      className="accountmenu"
      ref={picker.boxRef}
      onPointerEnter={(e) => {
        if (e.pointerType === "touch") return;
        if (!picker.open) {
          pinned.current = false;
          picker.openAt();
        }
      }}
      onPointerLeave={(e) => {
        if (e.pointerType === "touch") return;
        if (pinned.current) return;
        closeByPointer();
      }}
      // A click anywhere in the panel means the menu is being USED: pin it,
      // so a pointer drifting off while a segment is being read does not
      // take the panel with it.
      onClick={() => {
        if (picker.open) pinned.current = true;
      }}
    >
      <button
        ref={picker.triggerRef}
        type="button"
        className={"secondary icon-btn accountmenu-btn" + (picker.open ? " active" : "")}
        // A DISCLOSURE, NOT A `role="menu"`. The panel holds a link, a radio
        // group, a picker and a button — four native behaviours. Declaring it
        // a menu puts a screen reader into menu mode, where anything that is
        // not a `menuitem` goes unread, and the appearance segments and the
        // language list are exactly that. `aria-expanded` + `aria-controls`
        // is the pattern that describes what this actually is.
        aria-expanded={picker.open}
        aria-controls="accountmenu-pop"
        aria-label={label}
        onClick={() => {
          // Hovered-open + click = PIN, not toggle. Closing here would make a
          // click on a menu the pointer already opened read as "nothing
          // happened" (it reopens on the very next pointer event).
          if (picker.open && !pinned.current) {
            pinned.current = true;
            return;
          }
          if (picker.open) {
            pinned.current = false;
            picker.close();
            return;
          }
          openPinned();
        }}
        // KEYBOARD PARITY, the same gate QuickTip uses: `:focus-visible`, so
        // the focus a mouse click also produces does not fight the click
        // handler above.
        onFocus={(e) => {
          // ALREADY OPEN ⇒ NOTHING TO DO, and saying so explicitly is what
          // keeps Escape from reopening the panel: the dismissal stack closes
          // by calling usePicker's `close()`, which restores focus to this
          // very button in the same tick. Without this line that refocus
          // reads as "a keyboard arrived" and opens what was just dismissed.
          if (picker.open) return;
          let keyboard = false;
          try {
            keyboard = e.currentTarget.matches(":focus-visible");
          } catch {
            /* unknown to some test DOMs — then only click and hover open it,
               which is what happened before this handler existed */
          }
          if (keyboard) openPinned();
        }}
        onKeyDown={picker.onKeyDown}
      >
        <PersonIcon />
      </button>
      {picker.open && (
        <div
          className="accountmenu-pop"
          id="accountmenu-pop"
          aria-label={t("nav.account") as string}
        >
          {/* The identity row IS the door to the profile (ADR-46584 §2), and
              says so in words on its second line. */}
          <NavLink
            to="/profile"
            className="accountmenu-who"
            onClick={() => {
              pinned.current = false;
              picker.setOpen(false);
            }}
          >
            <span className="accountmenu-avatar" aria-hidden="true">{initials(displayName)}</span>
            <span className="accountmenu-id">
              <strong className="accountmenu-name">{displayName}</strong>
              <span className="accountmenu-sub">
                {/* THE ROLE IS AUTHORISATION, NOT DECORATION. Someone who does
                    not know which role they are acting under will reach for
                    the wrong gesture. */}
                {isGlobalAdmin ? (
                  <span className="badge admin">{t("common.globalAdmin")}</span>
                ) : (
                  siteRole && <span className="badge">{t(`common.role_${siteRole}`)}</span>
                )}
                <span className="accountmenu-go">{t("nav.myProfile")} ›</span>
              </span>
            </span>
          </NavLink>
          <hr className="accountmenu-sep" />
          {/* Two settings of the PERSON, as label/control rows: the label
              names the control, so the control can stay compact. */}
          <div className="accountmenu-row">
            <span className="accountmenu-lbl">{t("common.theme")}</span>
            <ThemeControl />
          </div>
          <div className="accountmenu-row">
            <span className="accountmenu-lbl">{t("common.language")}</span>
            <LanguageSelector labelled />
          </div>
          <hr className="accountmenu-sep" />
          <button
            type="button"
            className="accountmenu-out"
            onClick={() => {
              pinned.current = false;
              picker.setOpen(false);
              onLogout();
            }}
          >
            <LogoutIcon />
            <span>{t("common.logout")}</span>
          </button>
        </div>
      )}
    </div>
  );
}
