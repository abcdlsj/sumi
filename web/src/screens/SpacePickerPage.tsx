import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight } from "lucide-react";
import { useEffect } from "react";

import { ApiRequestError, listSpaces } from "../api/client";
import type { Space } from "../api/types";
import { SumiLogo } from "../components/SumiLogo";
import { OnboardingSkeleton } from "../components/Skeleton";
import { landing, landingPath } from "../landing";
import { accentTokens } from "../spaceAccent";

/**
 * Shown when the identity belongs to more than one Space. One Space would not be a question, so a
 * caller who has none or one is sent on by the same rule the entry point uses.
 */
export function SpacePickerPage() {
  const navigate = useNavigate();
  const spaces = useQuery({ queryKey: ["spaces"], queryFn: listSpaces, retry: false });
  const destination = spaces.isSuccess ? landing(spaces.data) : undefined;

  useEffect(() => {
    if (spaces.error instanceof ApiRequestError && spaces.error.status === 401) {
      void navigate({ to: "/login", search: { redirect: undefined }, replace: true });
      return;
    }
    if (!destination || destination.kind === "choose-space") return;
    const path = landingPath(destination);
    if (path) void navigate({ href: path, replace: true });
  }, [destination, navigate, spaces.error]);

  if (spaces.error && !(spaces.error instanceof ApiRequestError && spaces.error.status === 401)) {
    throw spaces.error;
  }
  if (!destination || destination.kind !== "choose-space" || !spaces.data) {
    return <OnboardingSkeleton label="Opening your Spaces" />;
  }

  return (
    <main className="onboarding-shell">
      <header className="brand-bar">
        <span className="wordmark">
          <span className="wordmark-mark" aria-hidden="true">
            <SumiLogo />
          </span>
          Sumi
        </span>
      </header>

      <section className="space-picker" aria-labelledby="choose-space-title">
        <h1 id="choose-space-title">Choose a Space.</h1>
        <p className="space-picker-note">
          Each Space has its own Channels, Members and Computers.
        </p>
        <ul className="space-picker-list">
          {spaces.data.map((space) => <SpaceOption key={space.id} space={space} />)}
        </ul>
        <p className="form-switch">
          Need another one? <Link to="/spaces/new">Create a Space</Link>
        </p>
      </section>

      <footer className="onboarding-footer" />
    </main>
  );
}

function SpaceOption({ space }: { space: Space }) {
  return (
    <li>
      <Link className="space-picker-option" to="/s/$spaceSlug" params={{ spaceSlug: space.slug }}>
        <span
          className="space-picker-mark"
          style={{ backgroundColor: space.accent, color: accentTokens(space.accent)?.["--on-accent"] }}
          aria-hidden="true"
        >
          <SumiLogo />
        </span>
        <span className="space-picker-copy">
          <strong>{space.name}</strong>
          <small>/s/{space.slug}</small>
        </span>
        <ArrowRight aria-hidden="true" />
      </Link>
    </li>
  );
}
