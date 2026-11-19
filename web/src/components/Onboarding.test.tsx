import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { api, type BootstrapData } from "../api";
import { capturePairingFromURL, readPendingPairing } from "../pairing";
import { Onboarding } from "./Onboarding";

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

const noop = () => {};
const user = { id: "u1", email: "li.si@example.com", display_name: "li_si", created_at: "" };
const emptyBoot = {
  space: { id: "sp", slug: "li-si-studio", name: "S", owner_member_id: "m", event_seq: 1, created_at: "" },
  current_member: { id: "m", space_id: "sp", kind: "human", display_name: "li_si", access_level: "owner", created_at: "" },
  members: [],
  conversations: [],
  inbox: [],
  tasks: [],
  computers: [],
} as BootstrapData;

afterEach(() => {
  vi.restoreAllMocks();
  sessionStorage.clear();
});

describe("Onboarding", () => {
  it("registers with a display name derived from the email", async () => {
    const register = vi.spyOn(api, "register").mockResolvedValue({ user });
    const onAuth = vi.fn();
    wrap(
      <Onboarding step="account" user={null} spaceSlug="" onAuthenticated={onAuth} onSpaceCreated={noop} onComputerDone={noop} onFinish={noop} onLogout={noop} />,
    );
    fireEvent.click(screen.getByRole("tab", { name: "Create account" }));
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "li.si@example.com" } });
    expect((screen.getByLabelText("Display name") as HTMLInputElement).value).toBe("li_si");
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "secret-123" } });
    fireEvent.submit(screen.getByLabelText("Password").closest("form")!);
    await waitFor(() => expect(register).toHaveBeenCalledWith("li.si@example.com", "secret-123", "li_si"));
    expect(onAuth).toHaveBeenCalled();
  });

  it("shows a wrong-password error inline on sign-in failure", async () => {
    const { ApiError } = await import("../api");
    vi.spyOn(api, "login").mockRejectedValue(new ApiError(401, "unauthenticated", "invalid credentials"));
    wrap(
      <Onboarding step="account" user={null} spaceSlug="" onAuthenticated={noop} onSpaceCreated={noop} onComputerDone={noop} onFinish={noop} onLogout={noop} />,
    );
    fireEvent.change(screen.getByLabelText("Email"), { target: { value: "a@b.co" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "wrongpass" } });
    fireEvent.submit(screen.getByLabelText("Password").closest("form")!);
    expect(await screen.findByText("Email or password is incorrect.")).toBeTruthy();
  });

  it("derives the space slug from its name", async () => {
    const create = vi.spyOn(api, "createSpace").mockResolvedValue({ space: { ...emptyBoot.space, slug: "acme-lab" }, member: emptyBoot.current_member });
    const onCreated = vi.fn();
    wrap(
      <Onboarding step="space" user={user} spaceSlug="" onAuthenticated={noop} onSpaceCreated={onCreated} onComputerDone={noop} onFinish={noop} onLogout={noop} />,
    );
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Acme Lab!" } });
    fireEvent.click(screen.getByRole("button", { name: "Create space" }));
    await waitFor(() => expect(create).toHaveBeenCalledWith("acme-lab", "Acme Lab!"));
    expect(onCreated).toHaveBeenCalledWith("acme-lab");
  });

  it("confirms a desktop pairing only after an explicit click", async () => {
    window.history.replaceState(null, "", "/?pair=p-1&code=K7QX4M");
    capturePairingFromURL();
    expect(window.location.search).toBe("");
    expect(readPendingPairing()).toEqual({ id: "p-1", code: "K7QX4M" });

    vi.spyOn(api, "bootstrap").mockResolvedValue(emptyBoot);
    vi.spyOn(api, "getPairing").mockResolvedValue({
      pairing: { id: "p-1", hostname: "studio.local", os: "macos", status: "pending", expires_at: "" },
    });
    const confirm = vi.spyOn(api, "confirmPairing").mockResolvedValue({
      computer: { id: "c1", space_id: "sp", name: "studio", hostname: "studio.local", os: "macos", connection_status: "offline", created_at: "" },
    });
    wrap(
      <Onboarding step="computer" user={user} spaceSlug="li-si-studio" onAuthenticated={noop} onSpaceCreated={noop} onComputerDone={noop} onFinish={noop} onLogout={noop} />,
    );
    const btn = await screen.findByRole("button", { name: "Connect this computer" });
    expect(screen.getByText("K7QX4M")).toBeTruthy();
    expect(confirm).not.toHaveBeenCalled();
    fireEvent.click(btn);
    await waitFor(() => expect(confirm).toHaveBeenCalledWith("li-si-studio", "p-1", "K7QX4M", "studio"));
    expect(readPendingPairing()).toBeNull();
  });
});
