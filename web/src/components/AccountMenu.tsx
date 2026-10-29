import { Link } from "@tanstack/react-router";
import { ChevronsUpDown, LogOut, Settings } from "lucide-react";
import { useEffect, useRef, useState } from "react";

import { PixelIdentity } from "./PixelIdentity";
import { useSignOut } from "./signOut";

/** The signed-in person in the sidebar footer: who you are, and how to leave. */
export function AccountMenu({ memberId, name, email, spaceSlug }: { memberId: string; name: string; email?: string; spaceSlug: string }) {
  const [open, setOpen] = useState(false);
  const { signOut, signingOut } = useSignOut();
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (!open) return;
    function onPointer(event: PointerEvent) {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    }
    function onKey(event: KeyboardEvent) {
      if (event.key !== "Escape") return;
      setOpen(false);
      triggerRef.current?.focus();
    }
    document.addEventListener("pointerdown", onPointer);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", onPointer);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  return (
    <div className="account-menu" ref={rootRef}>
      <button
        ref={triggerRef}
        className="account-menu-trigger"
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Account: ${name}`}
        onClick={() => setOpen((value) => !value)}
      >
        <PixelIdentity name={name} kind="human" seed={memberId} />
        <span>{name}</span>
        <ChevronsUpDown aria-hidden="true" />
      </button>
      {open ? (
        <div className="account-menu-popover" role="menu" aria-label="Account">
          <div className="account-menu-who">
            <strong>{name}</strong>
            {email ? <small>{email}</small> : null}
          </div>
          <Link role="menuitem" className="account-menu-item" to="/s/$spaceSlug/settings" params={{ spaceSlug }} onClick={() => setOpen(false)}>
            <Settings aria-hidden="true" /> Settings
          </Link>
          <button role="menuitem" className="account-menu-item" type="button" disabled={signingOut} onClick={() => void signOut()}>
            <LogOut aria-hidden="true" /> {signingOut ? "Signing out…" : "Sign out"}
          </button>
        </div>
      ) : null}
    </div>
  );
}
