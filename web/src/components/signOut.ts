import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import { logout } from "../api/client";
import { notify } from "../feedback";

/** Ends the browser Session and returns to the sign-in page. */
export function useSignOut() {
  const [signingOut, setSigningOut] = useState(false);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  async function signOut() {
    setSigningOut(true);
    try {
      await logout();
      queryClient.clear();
      await navigate({ to: "/login", search: { redirect: undefined } });
    } catch (error) {
      notify("error", `Could not sign out. ${error instanceof Error ? error.message : ""}`.trim());
      setSigningOut(false);
    }
  }
  return { signOut, signingOut };
}
