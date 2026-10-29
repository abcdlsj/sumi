import { useMutation } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ArrowRight, Asterisk, LoaderCircle } from "lucide-react";
import type { FormEvent } from "react";

import { ApiRequestError, register } from "../api/client";
import { SumiLogo } from "../components/SumiLogo";

export function RegisterPage() {
  const navigate = useNavigate();
  const { redirect } = useSearch({ from: "/" });
  const registration = useMutation({
    mutationFn: register,
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
    registration.mutate({
      display_name: String(form.get("displayName") ?? "").trim(),
      email: String(form.get("email") ?? "").trim(),
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
        <OnboardingProgress current={1} />
      </header>

      <section className="register-stage" aria-labelledby="register-title">
        <div className="stage-copy">
          <p className="eyebrow">Step 1 · Identity</p>
          <h1 id="register-title">Join your collaborators.</h1>
          <p className="stage-note">
            Create the Human identity you will use beside Agents. Your first Space comes next.
          </p>
          <div className="onboarding-identities" aria-hidden="true">
            <span className="onboarding-human-mark">H</span>
            <span className="onboarding-agent-mark"><Asterisk strokeWidth={3} /></span>
            <span className="onboarding-agent-mark onboarding-agent-mark--cyan"><Asterisk strokeWidth={3} /></span>
            <span className="signal-line" />
          </div>
        </div>

        <form className="register-form" onSubmit={submit}>
          <label>
            Display name
            <input name="displayName" autoComplete="name" autoFocus required maxLength={40} pattern="[\p{L}_]+" title="Letters and underscores only, no spaces" />
          </label>
          <label>
            Email
            <input name="email" type="email" autoComplete="email" required />
          </label>
          <div className="form-field">
            <label>
              Password
              <input
                name="password"
                type="password"
                autoComplete="new-password"
                minLength={12}
                aria-describedby="register-password-hint"
                required
              />
            </label>
            <span className="form-hint" id="register-password-hint">
              At least 12 characters.
            </span>
          </div>
          {registration.error ? (
            <p className="form-error" role="alert">
              {registrationErrorText(registration.error)}
            </p>
          ) : null}
          <button className="primary-action" type="submit" disabled={registration.isPending}>
            <span>Continue</span>
            {registration.isPending ? (
              <LoaderCircle className="spin" aria-hidden="true" />
            ) : (
              <ArrowRight aria-hidden="true" />
            )}
          </button>
          <p className="form-switch">
            Already a Member? <Link to="/login" search={{ redirect }}>Sign in</Link>
          </p>
        </form>
      </section>

      <footer className="onboarding-footer">
        <span>Humans and Agents, working together</span>
      </footer>
    </main>
  );
}

export function OnboardingProgress({ current }: { current: 1 | 2 }) {
  return (
    <ol className="onboarding-progress" aria-label={`Onboarding step ${current} of 3`}>
      <li className={current === 1 ? "is-current" : "is-complete"} aria-current={current === 1 ? "step" : undefined}>
        <span>01</span><strong>Identity</strong>
      </li>
      <li className={current === 2 ? "is-current" : ""} aria-current={current === 2 ? "step" : undefined}>
        <span>02</span><strong>Space</strong>
      </li>
      <li><span>03</span><strong>Setup</strong></li>
    </ol>
  );
}

// Registration fails for several causes, including an email that already exists. Keep every
// conflict on one message so a caller cannot tell which accounts are registered.
function registrationErrorText(error: Error): string {
  if (error instanceof ApiRequestError && error.status === 409) {
    return "Could not create the account. Check your details and try again.";
  }
  return error.message;
}

function isSafeRedirect(value: string | undefined): value is string {
  return Boolean(value?.startsWith("/") && !value.startsWith("//"));
}
