import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Attachment } from "../api";
import { resetLayers } from "../layers";
import { installWindowCloseHandler } from "../shell";
import { MessageAttachments } from "./Attachments";

const img1: Attachment = {
  id: "att-1",
  space_id: "s",
  uploader_member_id: "m1",
  name: "diagram.png",
  media_type: "image/png",
  size_bytes: 2048,
  created_at: "2026-11-20T10:00:00Z",
};

const img2: Attachment = {
  id: "att-2",
  space_id: "s",
  uploader_member_id: "m1",
  name: "architecture.png",
  media_type: "image/png",
  size_bytes: 4096,
  created_at: "2026-11-20T10:01:00Z",
};

const doc: Attachment = {
  id: "att-3",
  space_id: "s",
  uploader_member_id: "m1",
  name: "notes.md",
  media_type: "text/markdown",
  size_bytes: 128,
  created_at: "2026-11-20T10:02:00Z",
};

afterEach(() => {
  vi.restoreAllMocks();
  resetLayers();
  sessionStorage.clear();
});

describe("MessageAttachments", () => {
  it("opens an image in a lightbox dialog, supports gallery navigation and zoom, and closes on Escape", () => {
    render(<MessageAttachments spaceSlug="studio" attachments={[img1, img2]} />);

    fireEvent.click(screen.getByRole("button", { name: "Preview diagram.png" }));
    const dialog = screen.getByRole("dialog", { name: "diagram.png" });
    expect(dialog).toBeTruthy();
    expect(within(dialog).getByText("1 / 2")).toBeTruthy();
    expect(within(dialog).getByRole("link", { name: /Download/ }).getAttribute("href")).toBe("/api/v1/spaces/studio/attachments/att-1");

    fireEvent.keyDown(window, { key: "ArrowRight" });
    expect(screen.getByRole("dialog", { name: "architecture.png" })).toBeTruthy();
    expect(screen.getByText("2 / 2")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }));
    expect(screen.getByText("150%")).toBeTruthy();
    fireEvent.keyDown(window, { key: "0" });
    expect(screen.getByText("100%")).toBeTruthy();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders text and markdown attachments inline and opens the full preview modal from the preview button", async () => {
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      text: async () => "# Release Notes\n\nAll checks passed.",
    } as Response);

    render(<MessageAttachments spaceSlug="studio" attachments={[doc]} />);

    // Inline preview renders immediately in the message stream without opening a modal.
    await waitFor(() => expect(screen.getByText("Release Notes")).toBeTruthy());
    expect(screen.getByText("All checks passed.")).toBeTruthy();
    expect(screen.queryByRole("dialog")).toBeNull();

    // Clicking the preview button opens the modal and reuses the loaded content.
    fireEvent.click(screen.getByRole("button", { name: "Preview notes.md" }));
    const dialog = await screen.findByRole("dialog", { name: "notes.md" });
    expect(within(dialog).getByText("Release Notes")).toBeTruthy();
    expect(within(dialog).getByText("All checks passed.")).toBeTruthy();
    expect(fetchSpy).toHaveBeenCalledTimes(1);

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("renders agent-uploaded images inline even when media_type is application/octet-stream", () => {
    const octetImg: Attachment = { ...img1, id: "att-octet", name: "agent-chart.png", media_type: "application/octet-stream" };
    render(<MessageAttachments spaceSlug="studio" attachments={[octetImg]} />);
    expect(screen.getByAltText("agent-chart.png")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Preview agent-chart.png" }));
    expect(screen.getByRole("dialog", { name: "agent-chart.png" })).toBeTruthy();
  });
});

describe("closing overlays with the window shortcut", () => {
  it("closes the image lightbox on ⌘W instead of hiding the window", async () => {
    sessionStorage.setItem("sumi.shell", "desktop");
    sessionStorage.setItem("sumi.desktop.control", "http://127.0.0.1:54321");
    sessionStorage.setItem("sumi.desktop.token", "tok-123");
    const fetchSpy = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(JSON.stringify({ ok: true }), { status: 200 }));
    const cleanup = installWindowCloseHandler(window);

    render(<MessageAttachments spaceSlug="studio" attachments={[img1]} />);
    fireEvent.click(screen.getByRole("button", { name: "Preview diagram.png" }));
    expect(screen.getByRole("dialog", { name: "diagram.png" })).toBeTruthy();

    fireEvent.keyDown(window, { key: "w", metaKey: true });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(fetchSpy).not.toHaveBeenCalled();

    // With nothing left open the shortcut falls through to the window itself.
    fireEvent.keyDown(window, { key: "w", metaKey: true });
    await waitFor(() => expect(fetchSpy).toHaveBeenCalledTimes(1));
    cleanup();
  });
});
