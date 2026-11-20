import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { Attachment } from "../api";
import { MessageAttachments } from "./Attachments";

const img: Attachment = {
  id: "att-1",
  space_id: "s",
  uploader_member_id: "m1",
  name: "diagram.png",
  media_type: "image/png",
  size_bytes: 2048,
  created_at: "2026-11-20T10:00:00Z",
};

describe("MessageAttachments", () => {
  it("opens an image in a lightbox dialog and closes on Escape", () => {
    render(<MessageAttachments spaceSlug="studio" attachments={[img]} />);

    fireEvent.click(screen.getByRole("button", { name: "Preview diagram.png" }));
    const dialog = screen.getByRole("dialog", { name: "diagram.png" });
    expect(dialog).toBeTruthy();
    expect(screen.getByRole("link", { name: /Download/ }).getAttribute("href")).toBe("/api/v1/spaces/studio/attachments/att-1");

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
