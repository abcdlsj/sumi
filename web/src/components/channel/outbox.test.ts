import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import type { Message } from "../../api/client";
import { useOutbox } from "./outbox";

const input = { body_markdown: "Hello", mentions: [], mention_all: false, attachment_ids: [] };

describe("useOutbox", () => {
  it("shows a message at once and removes it once the Server confirms", async () => {
    let resolve: (message: Message) => void = () => undefined;
    const send = vi.fn(() => new Promise<Message>((done) => { resolve = done; }));
    const onSent = vi.fn();
    const { result } = renderHook(() => useOutbox(send, onSent));

    act(() => result.current.queue(input));
    expect(result.current.items).toMatchObject([{ status: "sending", input }]);

    await act(async () => resolve({ id: "m1" } as Message));
    expect(onSent).toHaveBeenCalledWith({ id: "m1" });
    expect(result.current.items).toEqual([]);
  });

  it("keeps a failed message for retry or discard", async () => {
    const send = vi.fn().mockRejectedValueOnce(new Error("Offline")).mockResolvedValueOnce({ id: "m2" } as Message).mockReturnValue(new Promise(() => undefined));
    const onSent = vi.fn();
    const { result } = renderHook(() => useOutbox(send, onSent));

    act(() => result.current.queue(input));
    await waitFor(() => expect(result.current.items[0]).toMatchObject({ status: "failed", error: "Offline" }));

    act(() => result.current.retry(result.current.items[0]));
    await waitFor(() => expect(result.current.items).toEqual([]));
    expect(onSent).toHaveBeenCalledWith({ id: "m2" });

    act(() => result.current.queue(input));
    act(() => result.current.discard(result.current.items[0].key));
    expect(result.current.items).toEqual([]);
  });
});
