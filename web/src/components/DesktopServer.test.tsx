import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { captureShellFromURL, getDesktopBridge } from "../shell";
import { DesktopServerTrigger } from "./DesktopServer";

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  vi.restoreAllMocks();
});

describe("DesktopServer", () => {
  it("captures bridge coordinates from URL fragment and clears them from the address bar", () => {
    window.history.replaceState(null, "", "/?shell=desktop#desktop_control=http%3A%2F%2F127.0.0.1%3A55123&desktop_token=tok123");
    captureShellFromURL();
    expect(window.location.search).toBe("");
    expect(window.location.hash).toBe("");
    expect(getDesktopBridge()).toEqual({ controlUrl: "http://127.0.0.1:55123", token: "tok123" });
  });

  it("renders nothing outside desktop shell", () => {
    const { container } = render(<DesktopServerTrigger variant="rail" />);
    expect(container.innerHTML).toBe("");
  });

  it("switches to a remote server and surfaces unreachable errors inline", async () => {
    sessionStorage.setItem("sumi.desktop.control", "http://127.0.0.1:55123");
    sessionStorage.setItem("sumi.desktop.token", "tok123");

    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            server_url: "http://127.0.0.1:8087",
            default_local_url: "http://127.0.0.1:8087",
            is_local: true,
            computer_status: "online",
          }),
          { status: 200 },
        ),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ error: "server_unreachable", message: "server unreachable: https://dead.team" }), {
          status: 502,
        }),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            server_url: "https://sumi.team",
            default_local_url: "http://127.0.0.1:8087",
            is_local: false,
            computer_status: "online",
          }),
          { status: 200 },
        ),
      );

    render(<DesktopServerTrigger variant="rail" />);
    const trigger = await screen.findByRole("button", { name: "Server: Local" });
    fireEvent.click(trigger);

    fireEvent.click(screen.getByRole("tab", { name: "Remote" }));
    const input = screen.getByLabelText("Server URL");
    fireEvent.change(input, { target: { value: "https://dead.team" } });
    fireEvent.click(screen.getByRole("button", { name: "Switch" }));

    expect((await screen.findByRole("alert")).textContent).toContain("server unreachable");

    fireEvent.change(input, { target: { value: "https://sumi.team" } });
    fireEvent.click(screen.getByRole("button", { name: "Switch" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Server: sumi.team" })).toBeTruthy();
    });
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });
});
