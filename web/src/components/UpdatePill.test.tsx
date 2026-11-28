import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { clearAllDraftsForTests, readAllDrafts, readDraft } from "../drafts";
import { clearPreferencesForTests, readLastSpace, readLastSpaceView } from "../preferences";
import { type DesktopUpdateStatus } from "../shell";
import {
  clearUpdateHandoffForTests,
  consumeStartupHandoff,
  FlushRegistry,
  takePendingReplyDraft,
  takePendingScrollRestore,
} from "../updateHandoff";
import { UpdatePill, useAutoUpdate } from "./UpdatePill";

function enableDesktopBridge() {
  sessionStorage.setItem("sumi.shell", "desktop");
  sessionStorage.setItem("sumi.desktop.control", "http://127.0.0.1:55123");
  sessionStorage.setItem("sumi.desktop.token", "tok123");
}

afterEach(() => {
  cleanup();
  sessionStorage.clear();
  clearAllDraftsForTests();
  clearPreferencesForTests();
  clearUpdateHandoffForTests();
  vi.restoreAllMocks();
});

describe("UpdatePill", () => {
  it("renders nothing outside desktop shell even when an update is available", () => {
    const status: DesktopUpdateStatus = {
      current: "6.0.8",
      latest: "6.0.9",
      available: true,
      state: "available",
      progress: 0,
      can_apply: true,
    };
    const { container } = render(<UpdatePill status={status} onActivate={() => {}} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders nothing in desktop shell when idle or checking", () => {
    enableDesktopBridge();
    const idle: DesktopUpdateStatus = {
      current: "6.0.8",
      available: false,
      state: "idle",
      progress: 0,
      can_apply: false,
    };
    const { container, rerender } = render(<UpdatePill status={idle} onActivate={() => {}} />);
    expect(container.innerHTML).toBe("");

    rerender(<UpdatePill status={{ ...idle, state: "checking" }} onActivate={() => {}} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders compact progress and ready states inside desktop shell", () => {
    enableDesktopBridge();
    const onActivate = vi.fn();
    const base: DesktopUpdateStatus = {
      current: "6.0.8",
      latest: "6.0.9",
      available: true,
      state: "available",
      progress: 0,
      can_apply: true,
    };

    const { rerender } = render(<UpdatePill status={base} onActivate={onActivate} />);
    const availBtn = screen.getByRole("button", { name: /Update to v6\.0\.9/ });
    expect(availBtn.textContent).toBe("↑ 6.0.9");
    fireEvent.click(availBtn);
    expect(onActivate).toHaveBeenCalledTimes(1);

    rerender(<UpdatePill status={{ ...base, state: "downloading", progress: 42 }} onActivate={onActivate} />);
    const dlBtn = screen.getByRole("button", { name: /Downloading v6\.0\.9 \(42%\)/ });
    expect(dlBtn.textContent).toBe("↓ 42%");
    expect((dlBtn as HTMLButtonElement).disabled).toBe(true);

    rerender(<UpdatePill status={{ ...base, state: "ready_to_restart", progress: 100 }} onActivate={onActivate} />);
    const readyBtn = screen.getByRole("button", { name: /v6\.0\.9 ready/ });
    expect(readyBtn.textContent).toBe("↻ 6.0.9");
    expect((readyBtn as HTMLButtonElement).disabled).toBe(false);
  });
});

describe("updateHandoff and Smart Restart Gate", () => {
  it("consumes startup handoff once and restores route, scroll, and unsaved drafts", async () => {
    enableDesktopBridge();
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValueOnce(
      new Response(
        JSON.stringify({
          handoff: {
            schema_version: 1,
            from_version: "6.0.8",
            to_version: "6.0.9",
            created_at_ms: Date.now(),
            ttl_ms: 120_000,
            app_state: {
              space_slug: "studio",
              main_view: { kind: "conversation", convID: "conv-42" },
              detail: { kind: "thread", threadConvID: "th-7", rootMessageID: "msg-1", parentSlug: "general" },
              scroll_conv_id: "conv-42",
              scroll_top: 320,
              drafts: {
                "conv-42": { text: "unsent-draft-token", replyTo: "msg-1" },
              },
              reply_box_draft: { key: "thread:th-7", text: "thread-draft-token" },
            },
          },
        }),
        { status: 200 },
      ),
    );

    const state = await consumeStartupHandoff();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(state?.space_slug).toBe("studio");
    expect(state?.detail).toEqual({
      kind: "thread",
      threadConvID: "th-7",
      rootMessageID: "msg-1",
      parentSlug: "general",
    });
    expect(readLastSpace()).toBe("studio");
    expect(readLastSpaceView("studio")).toEqual({ kind: "conversation", convID: "conv-42" });
    expect(takePendingScrollRestore("conv-42")).toBe(320);
    expect(takePendingScrollRestore("conv-42")).toBeNull();
    expect(readDraft("conv-42")).toEqual({ text: "unsent-draft-token", replyTo: "msg-1" });
    expect(readAllDrafts()["conv-42"]).toEqual({ text: "unsent-draft-token", replyTo: "msg-1" });
    expect(takePendingReplyDraft("thread:th-7")).toBe("thread-draft-token");
    expect(takePendingReplyDraft("thread:th-7")).toBeNull();
  });

  it("defers auto-restart when a participant blocks, then flushes and commits on click", async () => {
    enableDesktopBridge();
    const registry = new FlushRegistry();
    let blocking = true;
    const flushed = vi.fn(async () => {});
    registry.register({
      id: "test-composer",
      getBlockingReason: () => (blocking ? "Attachment upload in progress" : null),
      flushBeforeRestart: flushed,
      captureState: () => ({ scroll_conv_id: "conv-1", scroll_top: 180 }),
    });

    const toasts: string[] = [];
    const committedBodies: unknown[] = [];

    vi.spyOn(globalThis, "fetch").mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.endsWith("/desktop/update")) {
        return new Response(
          JSON.stringify({
            current: "6.0.8",
            latest: "6.0.9",
            available: true,
            state: "ready_to_restart",
            progress: 100,
            can_apply: true,
          }),
          { status: 200 },
        );
      }
      if (url.endsWith("/desktop/update/commit")) {
        committedBodies.push(JSON.parse(String(init?.body ?? "{}")));
        return new Response(JSON.stringify({ ok: true }), { status: 200 });
      }
      return new Response("{}", { status: 404 });
    });

    function Harness() {
      const { status, triggerUpdate } = useAutoUpdate({
        registry,
        captureRootState: () => ({ space_slug: "studio" }),
        onToast: (m) => toasts.push(m),
      });
      return <UpdatePill status={status} onActivate={() => void triggerUpdate()} />;
    }

    render(<Harness />);
    const readyBtn = await screen.findByRole("button", { name: /v6\.0\.9 ready/ });

    // Clicking while blocked warns via toast and does not commit.
    fireEvent.click(readyBtn);
    await waitFor(() => expect(toasts.length).toBe(1));
    expect(toasts[0]).toContain("Attachment upload in progress");
    expect(committedBodies.length).toBe(0);

    // Unblock and click again -> flushes and commits merged app_state.
    blocking = false;
    fireEvent.click(readyBtn);
    await waitFor(() => expect(committedBodies.length).toBe(1));
    expect(flushed).toHaveBeenCalledTimes(1);
    expect(committedBodies[0]).toEqual({
      app_state: {
        space_slug: "studio",
        scroll_conv_id: "conv-1",
        scroll_top: 180,
      },
    });
  });
});
