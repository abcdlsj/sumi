import { useState, type FormEvent, type ReactNode } from "react";
import { Check, Eye, EyeOff, Laptop } from "lucide-react";
import { api, ApiError, sanitizeDisplayName, slugInput, slugify, type User } from "../api";
import { readPendingPairing } from "../pairing";
import { AgentStep, ComputerStep } from "./Setup";
import { Button, Field, Segmented, SumiMark } from "./ui";

export type OnboardingStep = "account" | "space" | "computer" | "agent";
const STEPS: { key: OnboardingStep; label: string }[] = [
  { key: "account", label: "Account" },
  { key: "space", label: "Space" },
  { key: "computer", label: "Computer" },
  { key: "agent", label: "Agent" },
];

function Stepper({ current }: { current: OnboardingStep }) {
  const idx = STEPS.findIndex((s) => s.key === current);
  return (
    <ol className="flex items-center gap-2" aria-label="Setup progress">
      {STEPS.map((s, i) => {
        const done = i < idx;
        const active = i === idx;
        return (
          <li key={s.key} className="flex items-center gap-2 flex-1 min-w-0">
            <span
              className={`w-5 h-5 rounded-full text-[10.5px] font-semibold flex items-center justify-center shrink-0 transition-colors duration-200 ${
                done ? "bg-ink text-white" : active ? "border-[1.5px] border-ink text-ink" : "border border-field text-faint"
              }`}
              aria-current={active ? "step" : undefined}
            >
              {done ? <Check className="w-3 h-3" /> : i + 1}
            </span>
            <span className={`text-[12px] truncate ${active ? "text-ink font-medium" : "text-faint"}`}>{s.label}</span>
            {i < STEPS.length - 1 && (
              <span className="flex-1 h-px bg-line relative overflow-hidden">
                <span
                  className="absolute inset-0 bg-ink origin-left transition-transform duration-300 ease-out"
                  style={{ transform: `scaleX(${done ? 1 : 0})` }}
                />
              </span>
            )}
          </li>
        );
      })}
    </ol>
  );
}

function Shell({ step, children, footer }: { step: OnboardingStep; children: ReactNode; footer?: ReactNode }) {
  return (
    <div className="h-full overflow-y-auto bg-paper scroll-thin">
      <div className="min-h-full flex flex-col items-center justify-center px-4 py-10">
        <div className="w-full max-w-[440px]">
          <div className="flex items-center gap-2.5 mb-6">
            <SumiMark size={26} />
            <span className="text-[15px] font-semibold tracking-tight">Sumi</span>
            <span className="text-[12px] text-faint">Humans and agents, one space.</span>
          </div>
          <div className="mb-4">
            <Stepper current={step} />
          </div>
          <div key={step} className="bg-white border border-line-strong rounded-[10px] p-6 animate-rise-in">
            {children}
          </div>
          {footer && <div className="mt-4 text-center text-[12px] text-faint">{footer}</div>}
        </div>
      </div>
    </div>
  );
}

function fieldError(err: unknown): { field?: "email" | "password" | "display_name" | "slug"; text: string } {
  const text = err instanceof Error ? err.message : "Something went wrong";
  const lower = text.toLowerCase();
  if (err instanceof ApiError && err.status === 401) return { field: "password", text: "Email or password is incorrect." };
  if (lower.includes("password")) return { field: "password", text };
  if (lower.includes("display_name")) return { field: "display_name", text };
  if (lower.includes("email")) return { field: "email", text };
  if (lower.includes("slug")) return { field: "slug", text };
  return { text };
}

function PairingNotice() {
  if (!readPendingPairing()) return null;
  return (
    <div className="mb-4 px-3 py-2 rounded-[7px] border border-line bg-paper-2 text-[12px] text-ink-2 flex items-center gap-2">
      <Laptop className="w-3.5 h-3.5 shrink-0" />
      This computer is ready to connect. Sign in and confirm it in two steps.
    </div>
  );
}

function AccountStep({ onAuthenticated }: { onAuthenticated: () => void }) {
  const [mode, setMode] = useState<"login" | "register">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [nameTouched, setNameTouched] = useState(false);
  const [showPw, setShowPw] = useState(false);
  const [error, setError] = useState<ReturnType<typeof fieldError> | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      if (mode === "register") {
        await api.register(email.trim(), password, sanitizeDisplayName(displayName) || "me");
      } else {
        await api.login(email.trim(), password);
      }
      onAuthenticated();
    } catch (err) {
      setError(fieldError(err));
      setBusy(false);
    }
  }

  const errFor = (f: string) => (error?.field === f ? error.text : undefined);

  return (
    <form onSubmit={submit} className="space-y-4" noValidate={false}>
      <PairingNotice />
      <div>
        <h1 className="text-[17px] font-semibold text-ink">{mode === "login" ? "Welcome back" : "Create your account"}</h1>
        <p className="mt-0.5 text-[12.5px] text-mute">
          {mode === "login" ? "Sign in to continue to your spaces." : "It takes a minute. No email verification."}
        </p>
      </div>
      <Segmented
        size="md"
        value={mode}
        onChange={(m) => {
          setMode(m);
          setError(null);
        }}
        options={[
          { value: "login", label: "Sign in" },
          { value: "register", label: "Create account" },
        ]}
      />
      <Field
        label="Email"
        type="email"
        autoComplete="email"
        autoFocus
        value={email}
        onChange={(e) => {
          setEmail(e.target.value);
          if (!nameTouched) setDisplayName(sanitizeDisplayName(e.target.value.split("@")[0] ?? ""));
        }}
        error={errFor("email")}
        required
      />
      {mode === "register" && (
        <div className="animate-rise-in">
          <Field
            label="Display name"
            autoComplete="nickname"
            value={displayName}
            onChange={(e) => {
              setNameTouched(true);
              setDisplayName(e.target.value);
            }}
            onBlur={() => setDisplayName(sanitizeDisplayName(displayName))}
            hint="How teammates @mention you. Letters, digits and underscores."
            error={errFor("display_name")}
            required
          />
        </div>
      )}
      <Field
        label="Password"
        type={showPw ? "text" : "password"}
        autoComplete={mode === "login" ? "current-password" : "new-password"}
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        minLength={8}
        hint={mode === "register" ? "At least 8 characters." : undefined}
        error={errFor("password")}
        required
        trailing={
          <button
            type="button"
            onClick={() => setShowPw((v) => !v)}
            aria-label={showPw ? "Hide password" : "Show password"}
            className="w-7 h-7 rounded-[5px] text-faint hover:text-ink flex items-center justify-center"
          >
            {showPw ? <EyeOff className="w-3.5 h-3.5" /> : <Eye className="w-3.5 h-3.5" />}
          </button>
        }
      />
      {error && !error.field && (
        <div className="px-3 py-2 rounded-[6px] bg-danger-bg text-danger text-[12px] animate-fade-in">{error.text}</div>
      )}
      <Button type="submit" size="md" loading={busy} className="w-full">
        {mode === "login" ? "Sign in" : "Create account"}
      </Button>
    </form>
  );
}

function SpaceStep({ user, onCreated }: { user: User; onCreated: (slug: string) => void }) {
  const [name, setName] = useState(`${user.display_name}'s Studio`);
  const [slug, setSlug] = useState(slugify(`${user.display_name} studio`));
  const [slugTouched, setSlugTouched] = useState(false);
  const [error, setError] = useState<ReturnType<typeof fieldError> | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const res = await api.createSpace(slugify(slug), name.trim());
      onCreated(res.space.slug);
    } catch (err) {
      setError(fieldError(err));
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div>
        <h1 className="text-[17px] font-semibold text-ink">Create a space</h1>
        <p className="mt-0.5 text-[12.5px] text-mute">
          A space holds your channels, tasks, agents and computers. Invite people later.
        </p>
      </div>
      <Field
        label="Name"
        autoFocus
        value={name}
        onChange={(e) => {
          setName(e.target.value);
          if (!slugTouched) setSlug(slugify(e.target.value));
        }}
        required
      />
      <Field
        label="Address"
        value={slug}
        onChange={(e) => {
          setSlugTouched(true);
          setSlug(slugInput(e.target.value));
        }}
        className="font-mono-num"
        hint={
          <>
            Used in links: <span className="font-mono-num text-ink-2">/{slug || "…"}</span>
          </>
        }
        error={error?.field === "slug" ? error.text : undefined}
        required
      />
      {error && error.field !== "slug" && (
        <div className="px-3 py-2 rounded-[6px] bg-danger-bg text-danger text-[12px] animate-fade-in">{error.text}</div>
      )}
      <Button type="submit" size="md" loading={busy} disabled={!slug || !name.trim()} className="w-full">
        Create space
      </Button>
    </form>
  );
}

export function Onboarding({
  step,
  user,
  spaceSlug,
  onAuthenticated,
  onSpaceCreated,
  onComputerDone,
  onFinish,
  onLogout,
}: {
  step: OnboardingStep;
  user: User | null;
  spaceSlug: string;
  onAuthenticated: () => void;
  onSpaceCreated: (slug: string) => void;
  onComputerDone: () => void;
  onFinish: (agentMemberID?: string) => void;
  onLogout: () => void;
}) {
  const footer = user ? (
    <span>
      Signed in as <span className="text-ink-2">{user.email}</span> ·{" "}
      <button onClick={onLogout} className="underline decoration-field underline-offset-2 hover:text-ink">
        Sign out
      </button>
    </span>
  ) : undefined;

  return (
    <Shell step={step} footer={footer}>
      {step === "account" && <AccountStep onAuthenticated={onAuthenticated} />}
      {step === "space" && user && <SpaceStep user={user} onCreated={onSpaceCreated} />}
      {step === "computer" && <ComputerStep spaceSlug={spaceSlug} onNext={onComputerDone} />}
      {step === "agent" && (
        <AgentStep spaceSlug={spaceSlug} onDone={(id) => onFinish(id)} onSkip={() => onFinish()} />
      )}
    </Shell>
  );
}
