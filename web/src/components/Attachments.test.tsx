import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Attachment } from "../api";
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

afterEach(() => vi.restoreAllMocks());

describe("MessageAttachments", () => {
  it("opens an image in a lightbox dialog, supports gallery navigation and zoom, and closes on Escape", () => {
    render(<MessageAttachments spaceSlug="studio" attachments={[img1, img2]} />);

    fireEvent.click(screen.getByRole("button", { name: "Preview diagram.png" }));
    expect(screen.getByRole("dialog", { name: "diagram.png" })).toBeTruthy();
    expect(screen.getByText("1 / 2")).toBeTruthy();
    expect(screen.getByRole("link", { name: /Download/ }).getAttribute("href")).toBe("/api/v1/spaces/studio/attachments/att-1");

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

  it("previews text and markdown attachments inline inside a modal", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue({
      ok: true,
      text: async () => "# Release Notes\n\nAll checks passed.",
    } as Response);

    render(<MessageAttachments spaceSlug="studio" attachments={[doc]} />);

    fireEvent.click(screen.getByRole("button", { name: "Preview notes.md" }));
    const dialog = await screen.findByRole("dialog", { name: "notes.md" });
    expect(dialog).toBeTruthy();
    await waitFor(() => expect(screen.getByText("Release Notes")).toBeTruthy());
    expect(screen.getByText("All checks passed.")).toBeTruthy();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
