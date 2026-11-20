import { useState, type FormEvent, type ReactNode } from "react";
import { AtSign, Check, Eye, EyeOff, Laptop, Lock, Mail, User as UserIcon } from "lucide-react";
import { api, ApiError, sanitizeDisplayName, slugInput, slugify, type User } from "../api";
import { readPendingPairing } from "../pairing";
import { agentSprite, PixelAvatar } from "./Avatar";
import { DesktopServerTrigger } from "./DesktopServer";
import { AgentStep, ComputerStep, StepHeading } from "./Setup";
import { Button, Field, Segmented, SumiMark } from "./ui";

export type OnboardingStep = "account" | "space" | "computer" | "agent";
const STEPS: { key: OnboardingStep; label: string; hint: string }[] = [
  { key: "account", label: "Account", hint: "Sign in or register" },
  { key: "space", label: "Space", hint: "Where your team works" },
  { key: "computer", label: "Computer", hint: "Where agents run" },
  { key: "agent", label: "First agent", hint: "Your first teammate" },
];

function Stepper({ current }: { current: OnboardingStep }) {
  const idx = STEPS.findIndex((s) => s.key === current);
  return (
    <ol className="space-y-1" aria-label="Setup progress">
      {STEPS.map((s, i) => {
        const done = i < idx;
        const active = i === idx;
        return (
          <li key={s.key} className="flex items-center gap-3 py-2" aria-current={active ? "step" : undefined}>
            <span
              className={`size-7 rounded-full text-[12.5px] font-semibold flex items-center justify-center shrink-0 transition-colors duration-200 ${
                done ? "bg-accent text-accent-fg" : active ? "bg-nav-fg-strong text-nav" : "bg-nav-fill text-nav-fg-3 border border-nav-line"
              }`}
            >
              {done ? <Check className="size-3.5" /> : i + 1}
            </span>
            <span className="min-w-0">
              <span className={`block text-[14px] leading-5 ${active ? "text-nav-fg-strong font-medium" : done ? "text-nav-fg" : "text-nav-fg-3"}`}>{s.label}</span>
              <span className="block text-[12.5px] text-nav-fg-3">{s.hint}</span>
            </span>
          </li>
        );
      })}
    </ol>
  );
}

/** Six random seeds whose sprites all have different hues, so the row reads as a varied crew. */
function crewSeeds(): string[] {
  const seeds: string[] = [];
  const hues = new Set<number>();
  for (let i = 0; seeds.length < 6 && i < 200; i++) {
    const seed = Math.random().toString(36).slice(2);
    const { hue } = agentSprite(seed);
    if (hues.has(hue)) continue;
    hues.add(hue);
    seeds.push(seed);
  }
  return seeds;
}

function BrandPanel({ step }: { step: OnboardingStep }) {
  const [seeds] = useState(crewSeeds);
  return (
    <aside className="hidden lg:flex w-[400px] shrink-0 bg-nav text-nav-fg flex-col justify-between p-10 relative overflow-hidden">
      <div
        aria-hidden
        className="absolute -top-32 -right-24 size-[380px] rounded-full opacity-25 dark:opacity-40 blur-3xl"
        style={{ background: "radial-gradient(circle, var(--accent) 0%, transparent 65%)" }}
      />
      <div className="relative">
        <div className="flex items-center gap-3">
          <SumiMark size={36} />
          <span className="text-[19px] font-semibold tracking-tight text-nav-fg-strong">Sumi</span>
        </div>
        <p className="mt-6 font-display text-[27px] leading-[1.2] font-semibold text-nav-fg-strong">
          One workspace for people
          <br />
          and persistent agents.
        </p>
        <p className="mt-3 text-[14px] text-nav-fg-2 leading-relaxed">
          Agents keep their memory, pick up Tasks, and reply in channels and DMs like any teammate.
        </p>
        <div className="mt-8 flex gap-2.5" aria-hidden>
          {seeds.map((s, i) => (
            <span key={s} className="animate-rise-in" style={{ animationDelay: `${i * 60}ms` }}>
              <PixelAvatar seed={s} size="lg" />
            </span>
          ))}
        </div>
      </div>
      <div className="relative space-y-4">
        <Stepper current={step} />
        <DesktopServerTrigger variant="pill" />
      </div>
    </aside>
  );
}

function MobileSteps({ step }: { step: OnboardingStep }) {
  const idx = STEPS.findIndex((s) => s.key === step);
  return (
    <div className="lg:hidden mb-6 flex items-center gap-3">
      <SumiMark size={30} />
      <div className="flex-1 flex gap-1.5">
        {STEPS.map((s, i) => (
          <span key={s.key} className={`h-1 flex-1 rounded-full transition-colors ${i <= idx ? "bg-accent" : "bg-line-strong"}`} />
        ))}
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

function FormError({ text }: { text: string }) {
  return <div className="px-3.5 py-2.5 rounded-[10px] bg-danger-soft text-danger text-[13.5px] animate-fade-in">{text}</div>;
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
  const pairing = readPendingPairing();

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      if (mode === "register") await api.register(email.trim(), password, sanitizeDisplayName(displayName) || "me");
      else await api.login(email.trim(), password);
      onAuthenticated();
    } catch (err) {
      setError(fieldError(err));
      setBusy(false);
    }
  }

  const errFor = (f: string) => (error?.field === f ? error.text : undefined);

  return (
    <form onSubmit={submit} className="space-y-5">
      {pairing && (
        <div className="px-4 py-3 rounded-[12px] bg-accent-soft text-accent-text text-[13.5px] flex items-center gap-2.5">
          <Laptop className="size-4 shrink-0" />
          This computer is ready to connect. Sign in, then confirm it.
        </div>
      )}
      <div>
        <h1 className="text-[24px] font-semibold text-fg tracking-tight">{mode === "login" ? "Welcome back" : "Create your account"}</h1>
        <p className="mt-1.5 text-[14.5px] text-fg-3">
          {mode === "login" ? "Sign in to continue to your spaces." : "Takes a minute. No email verification needed."}
        </p>
      </div>
      <Segmented
        full
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
        leading={<Mail />}
        value={email}
        placeholder="you@example.com"
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
            leading={<AtSign />}
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
        leading={<Lock />}
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
            className="size-7 rounded-[6px] text-fg-3 hover:text-fg hover:bg-hover flex items-center justify-center"
          >
            {showPw ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
          </button>
        }
      />
      {error && !error.field && <FormError text={error.text} />}
      <Button type="submit" size="lg" loading={busy} className="w-full">
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
    <form onSubmit={submit} className="space-y-6">
      <StepHeading
        icon={<UserIcon />}
        title="Create a space"
        body="A space holds your channels, tasks, agents and computers. You can invite people later."
      />
      <div className="flex items-center gap-3.5 p-4 rounded-[14px] border border-line bg-subtle">
        <span className="size-12 rounded-[12px] bg-accent text-white text-[20px] font-semibold flex items-center justify-center shrink-0">
          {(name.trim()[0] ?? "S").toUpperCase()}
        </span>
        <div className="min-w-0">
          <div className="text-[15px] font-semibold text-fg truncate">{name.trim() || "Untitled space"}</div>
          <div className="text-[13px] text-fg-3 font-mono truncate">/{slug || "…"}</div>
        </div>
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
        className="font-mono"
        hint="Lowercase letters, digits and hyphens."
        error={error?.field === "slug" ? error.text : undefined}
        required
      />
      {error && error.field !== "slug" && <FormError text={error.text} />}
      <Button type="submit" size="lg" loading={busy} disabled={!slug || !name.trim()} className="w-full">
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
  let body: ReactNode = null;
  if (step === "account") body = <AccountStep onAuthenticated={onAuthenticated} />;
  if (step === "space" && user) body = <SpaceStep user={user} onCreated={onSpaceCreated} />;
  if (step === "computer") body = <ComputerStep spaceSlug={spaceSlug} onNext={onComputerDone} />;
  if (step === "agent") body = <AgentStep spaceSlug={spaceSlug} onDone={(a) => onFinish(a.id)} onSkip={() => onFinish()} />;

  return (
    <div className="h-full flex bg-bg">
      <BrandPanel step={step} />
      <main className="flex-1 min-w-0 overflow-y-auto scroll-thin">
        <div className="min-h-full flex flex-col px-6 py-10">
          <div className="flex-1 flex items-center justify-center">
            <div className="w-full max-w-[460px]">
              <MobileSteps step={step} />
              <div key={step} className="animate-rise-in">
                {body}
              </div>
            </div>
          </div>
          {user && (
            <div className="text-center text-[13px] text-fg-3 pt-8">
              Signed in as <span className="text-fg-2 font-medium">{user.email}</span>
              <span className="mx-2 text-fg-4">·</span>
              <button onClick={onLogout} className="hover:text-fg underline decoration-line-strong underline-offset-2">
                Sign out
              </button>
            </div>
          )}
        </div>
      </main>
    </div>
  );
}
