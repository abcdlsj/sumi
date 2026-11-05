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

  it("retries a failed message with the key it was queued with", async () => {
    const send = vi.fn().mockRejectedValueOnce(new Error("Offline")).mockResolvedValueOnce({ id: "m3" } as Message);
    const { result } = renderHook(() => useOutbox(send, () => undefined));

    act(() => result.current.queue(input));
    await waitFor(() => expect(result.current.items[0]).toMatchObject({ status: "failed" }));

    act(() => result.current.retry(result.current.items[0]));
    await waitFor(() => expect(result.current.items).toEqual([]));

    expect(send).toHaveBeenCalledTimes(2);
    expect(send.mock.calls[0]?.[0]).toEqual(input);
    expect(send.mock.calls[1]?.[1]).toBe(send.mock.calls[0]?.[1]);
  });

  it("gives each queued message its own idempotency key", () => {
    const send = vi.fn().mockReturnValue(new Promise<Message>(() => undefined));
    const { result } = renderHook(() => useOutbox(send, () => undefined));

    act(() => result.current.queue(input));
    act(() => result.current.queue(input));

    const keys = send.mock.calls.map((call) => call[1]);
    expect(keys).toHaveLength(2);
    expect(keys[0]).not.toBe(keys[1]);
    for (const key of keys) expect(key).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  });
});
