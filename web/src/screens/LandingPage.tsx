import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useRef } from "react";

import { ApiRequestError, listSpaces } from "../api/client";
import { OnboardingSkeleton } from "../components/Skeleton";
import { isSafeRedirect, landing, landingPath } from "../landing";
import { RegisterPage } from "./RegisterPage";

/**
 * `/` answers for every visitor: without a Session it is the registration form, with one Space it
 * opens that Space, with several it asks which one, and with none it offers to create one.
 */
export function LandingPage() {
  const { redirect } = useSearch({ from: "/" });
  const navigate = useNavigate();
  const spaces = useQuery({ queryKey: ["spaces"], queryFn: listSpaces, retry: false });
  const signedOut = spaces.error instanceof ApiRequestError && spaces.error.status === 401;
  const destination = spaces.isSuccess ? landing(spaces.data) : undefined;
  const leaving = useRef(false);

  const target = destination && (isSafeRedirect(redirect) ? redirect : landingPath(destination));
  useEffect(() => {
    if (!target || leaving.current) return;
    leaving.current = true;
    void navigate({ href: target, replace: true });
  }, [navigate, target]);

  if (signedOut) return <RegisterPage />;
  if (spaces.error) throw spaces.error;
  return <OnboardingSkeleton label="Opening Sumi" />;
}
