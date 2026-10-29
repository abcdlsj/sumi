import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowRight, LoaderCircle } from "lucide-react";
import type { FormEvent } from "react";

import { login } from "../api/client";
import { SumiLogo } from "../components/SumiLogo";

export function LoginPage() {
  const navigate = useNavigate();
  const { redirect } = useSearch({ from: "/login" });
  const authentication = useMutation({
    mutationFn: login,
    onSuccess: () => {
      if (isSafeRedirect(redirect)) {
        void navigate({ href: redirect });
      } else {
        void navigate({ to: "/spaces/new" });
      }
    },
  });

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    authentication.mutate({
      email: String(form.get("email") ?? ""),
      password: String(form.get("password") ?? ""),
    });
  }

  return (
    <main className="onboarding-shell">
      <header className="brand-bar">
        <Link className="wordmark" to="/" search={{ redirect: undefined }} aria-label="Sumi home">
          <span className="wordmark-mark" aria-hidden="true">
            <SumiLogo />
          </span>
          Sumi
        </Link>
        <span className="step-index">Welcome back</span>
      </header>

      <section className="register-stage" aria-labelledby="login-title">
        <div className="stage-copy stage-copy--login">
          <p className="eyebrow">Return to the room</p>
          <h1 id="login-title">Sign in.</h1>
          <p className="stage-note">Your Spaces are waiting where you left them.</p>
        </div>

        <form className="register-form" onSubmit={submit}>
          <label>
            Email
            <input name="email" type="email" autoComplete="email" required />
          </label>
          <label>
            Password
            <input name="password" type="password" autoComplete="current-password" required />
          </label>
          {authentication.error ? (
            <p className="form-error" role="alert">
              {authentication.error.message}
            </p>
          ) : null}
          <button className="primary-action" type="submit" disabled={authentication.isPending}>
            <span>Sign in</span>
            {authentication.isPending ? (
              <LoaderCircle className="spin" aria-hidden="true" />
            ) : (
              <ArrowRight aria-hidden="true" />
            )}
          </button>
          <p className="form-switch">
            New to Sumi? <Link to="/" search={{ redirect }}>Register</Link>
          </p>
        </form>
      </section>

      <footer className="onboarding-footer">
        <span>One identity, many Spaces</span>
      </footer>
    </main>
  );
}

function isSafeRedirect(value: string | undefined): value is string {
  return Boolean(value?.startsWith("/") && !value.startsWith("//"));
}
