import type { Space } from "./api/types";

/**
 * Where an entry point sends a caller who may or may not have a Session.
 *
 * A Space decides which Channels, Members and Computers exist, so the answer must be one Space
 * before any page can render. One Space is not a choice; several are.
 */
export type Landing =
  | { kind: "register" }
  | { kind: "create-space" }
  | { kind: "choose-space" }
  | { kind: "space"; spaceSlug: string };

/** `spaces` is the answer of `GET /api/v1/spaces`; `undefined` means the caller is signed out. */
export function landing(spaces: Space[] | undefined): Landing {
  if (!spaces) return { kind: "register" };
  const first = spaces[0];
  if (!first) return { kind: "create-space" };
  if (spaces.length === 1) return { kind: "space", spaceSlug: first.slug };
  return { kind: "choose-space" };
}

/** The path for a destination, so a caller can route without repeating the branch. */
export function landingPath(destination: Landing): string | undefined {
  switch (destination.kind) {
    case "create-space":
      return "/spaces/new";
    case "choose-space":
      return "/spaces";
    case "space":
      return `/s/${destination.spaceSlug}`;
    case "register":
      return undefined;
  }
}

/** Only a path inside the app may be followed after signing in or registering. */
export function isSafeRedirect(value: string | undefined): value is string {
  return Boolean(value?.startsWith("/") && !value.startsWith("//"));
}
