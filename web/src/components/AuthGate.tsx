import { useState, type FormEvent } from "react";
import { api, type Space, type User } from "../api";

export function AuthGate({
  user,
  spaces,
  onAuthenticated,
  onSpaceReady,
}: {
  user: User | null;
  spaces: Space[];
  onAuthenticated: () => void;
  onSpaceReady: (slug: string) => void;
}) {
  const [mode, setMode] = useState<"login" | "register">("register");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [spaceName, setSpaceName] = useState("Sumi Studio");
  const [spaceSlug, setSpaceSlug] = useState("studio");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function handleAuth(e: FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      if (mode === "register") {
        await api.register(email, password, displayName);
      } else {
        await api.login(email, password);
      }
      onAuthenticated();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Authentication failed");
    } finally {
      setBusy(false);
    }
  }

  async function handleCreateSpace(e: FormEvent) {
    e.preventDefault();
    setError("");
    setBusy(true);
    try {
      const res = await api.createSpace(spaceSlug, spaceName);
      onSpaceReady(res.space.slug);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Could not create space");
    } finally {
      setBusy(false);
    }
  }

  if (user && spaces.length === 0) {
    return (
      <div className="h-full bg-[#F4F2EC] flex items-center justify-center p-4">
        <form onSubmit={handleCreateSpace} className="w-full max-w-sm bg-white border border-[#DCD9D0] rounded-lg p-5 space-y-3.5">
          <div className="flex items-center gap-2">
            <span className="w-6 h-6 rounded-[5px] bg-[#161614] text-white text-xs font-semibold flex items-center justify-center">S</span>
            <h1 className="text-[15px] font-semibold text-[#161614]">Create Space</h1>
          </div>
          {error && <div className="px-2.5 py-1.5 rounded bg-[#FDF1EE] text-[#A83224] text-[12px]">{error}</div>}
          <div className="space-y-1">
            <label className="text-[12px] text-[#5C5A54]">Name</label>
            <input
              value={spaceName}
              onChange={(e) => setSpaceName(e.target.value)}
              className="w-full h-8 px-2.5 rounded border border-[#D5D1C7] focus:border-[#161614] outline-none text-[13px]"
              required
            />
          </div>
          <div className="space-y-1">
            <label className="text-[12px] text-[#5C5A54]">Slug</label>
            <input
              value={spaceSlug}
              onChange={(e) => setSpaceSlug(e.target.value)}
              className="w-full h-8 px-2.5 rounded border border-[#D5D1C7] focus:border-[#161614] outline-none text-[13px] font-mono-num"
              required
            />
          </div>
          <button
            disabled={busy}
            type="submit"
            className="w-full h-8 rounded bg-[#161614] hover:bg-[#2F2E2B] text-white text-[13px] font-medium disabled:opacity-50"
          >
            {busy ? "Creating…" : "Create Space"}
          </button>
        </form>
      </div>
    );
  }

  return (
    <div className="h-full bg-[#F4F2EC] flex items-center justify-center p-4">
      <form onSubmit={handleAuth} className="w-full max-w-sm bg-white border border-[#DCD9D0] rounded-lg p-5 space-y-3.5">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <span className="w-6 h-6 rounded-[5px] bg-[#161614] text-white text-xs font-semibold flex items-center justify-center">S</span>
            <h1 className="text-[15px] font-semibold text-[#161614]">
              {mode === "register" ? "Create account" : "Sign in to Sumi"}
            </h1>
          </div>
          <button
            type="button"
            onClick={() => setMode(mode === "register" ? "login" : "register")}
            className="text-[12px] text-[#5C5A54] hover:text-[#161614] underline decoration-[#D5D1C7]"
          >
            {mode === "register" ? "Sign in" : "Register"}
          </button>
        </div>
        {error && <div className="px-2.5 py-1.5 rounded bg-[#FDF1EE] text-[#A83224] text-[12px]">{error}</div>}
        {mode === "register" && (
          <div className="space-y-1">
            <label className="text-[12px] text-[#5C5A54]">Display name</label>
            <input
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder="Lisi"
              className="w-full h-8 px-2.5 rounded border border-[#D5D1C7] focus:border-[#161614] outline-none text-[13px]"
              required
            />
          </div>
        )}
        <div className="space-y-1">
          <label className="text-[12px] text-[#5C5A54]">Email</label>
          <input
            type="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className="w-full h-8 px-2.5 rounded border border-[#D5D1C7] focus:border-[#161614] outline-none text-[13px]"
            required
          />
        </div>
        <div className="space-y-1">
          <label className="text-[12px] text-[#5C5A54]">Password</label>
          <input
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            minLength={8}
            className="w-full h-8 px-2.5 rounded border border-[#D5D1C7] focus:border-[#161614] outline-none text-[13px]"
            required
          />
        </div>
        <button
          disabled={busy}
          type="submit"
          className="w-full h-8 rounded bg-[#161614] hover:bg-[#2F2E2B] text-white text-[13px] font-medium disabled:opacity-50"
        >
          {busy ? "Working…" : mode === "register" ? "Create account" : "Sign in"}
        </button>
      </form>
    </div>
  );
}
