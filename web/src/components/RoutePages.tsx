import { type ErrorComponentProps, useLocation, useRouter } from "@tanstack/react-router";
import { RotateCcw } from "lucide-react";

import { SumiLogo } from "./SumiLogo";

/** Shown for any address the app does not know. */
export function NotFoundPage() {
  const home = useHome();
  return (
    <main className="route-page" id="main-content">
      <SumiLogo className="route-page-logo" />
      <p className="route-page-code">404</p>
      <h1>This page does not exist</h1>
      <p>The link may be old, or the Channel, Task or Agent may have been removed.</p>
      <div className="route-page-actions">
        <a className="command-button command-button--accent" href={home.href}>{home.label}</a>
      </div>
    </main>
  );
}

/** Shown when a page fails to render, instead of a blank screen. */
export function RouteErrorPage({ error, reset }: ErrorComponentProps) {
  const router = useRouter();
  const home = useHome();
  return (
    <main className="route-page" id="main-content" role="alert">
      <SumiLogo className="route-page-logo" />
      <h1>Something went wrong on this page</h1>
      <p>Your messages and Tasks are safe. Try again, or start over from the Space.</p>
      {error?.message ? <code className="route-page-detail">{error.message}</code> : null}
      <div className="route-page-actions">
        <button className="command-button command-button--accent" type="button" onClick={() => { reset(); void router.invalidate(); }}>
          <RotateCcw aria-hidden="true" /> Try again
        </button>
        <a className="command-button" href={home.href}>{home.label}</a>
      </div>
    </main>
  );
}

/** Inside a Space the way back is that Space; anywhere else it is sign-in. */
function useHome(): { href: string; label: string } {
  const pathname = useLocation({ select: (location) => location.pathname });
  const space = /^\/s\/([^/]+)/.exec(pathname)?.[1];
  return space ? { href: `/s/${space}`, label: "Back to the Space" } : { href: "/login", label: "Go to sign in" };
}
