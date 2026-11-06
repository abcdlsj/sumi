import { afterEach, describe, expect, it, vi } from "vitest";

import { ApiRequestError, createMessage, createThreadReply, login } from "./client";

const channelId = "019c0000-0000-7000-8000-000000000003";
const threadId = "019c0000-0000-7000-8000-000000000004";
const idempotencyKey = "019c0000-0000-7000-8000-0000000000aa";
const input = { body_markdown: "Hello", mentions: [], mention_all: false, attachment_ids: [] };

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("message writes", () => {
  it("sends the caller's idempotency key for a Channel Message", async () => {
    const fetchMock = vi.fn(async () => json({ id: "m1" }, 201));
    vi.stubGlobal("fetch", fetchMock);

    await createMessage(channelId, input, idempotencyKey);

    expect(sentHeaders(fetchMock).get("idempotency-key")).toBe(idempotencyKey);
  });

  it("sends the caller's idempotency key for a Thread reply", async () => {
    const fetchMock = vi.fn(async () => json({ id: "m2" }, 201));
    vi.stubGlobal("fetch", fetchMock);

    await createThreadReply(threadId, { ...input, reply_to_message_id: "m0" }, idempotencyKey);

    expect(sentHeaders(fetchMock).get("idempotency-key")).toBe(idempotencyKey);
  });
});

describe("API errors", () => {
  it("keeps the Server's error code and message", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => json(
      { error: { code: "conflict", message: "Display Name is already taken", retryable: false } },
      409,
    )));

    const failure = await login({ email: "ada@example.test", password: "guess" }).catch((error: unknown) => error);

    expect(failure).toBeInstanceOf(ApiRequestError);
    expect((failure as ApiRequestError).code).toBe("conflict");
    expect((failure as ApiRequestError).message).toBe("Display Name is already taken");
  });
});

describe("rate limits", () => {
  it("turns a rate-limited sign-in into a retry time the reader can act on", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(
      JSON.stringify({ error: { code: "rate_limited", message: "too many attempts; retry later", retryable: true } }),
      { status: 429, headers: { "Content-Type": "application/json", "Retry-After": "61" } },
    )));

    const failure = await login({ email: "ada@example.test", password: "guess" }).catch((error: unknown) => error);

    expect(failure).toBeInstanceOf(ApiRequestError);
    expect((failure as ApiRequestError).code).toBe("rate_limited");
    expect((failure as ApiRequestError).status).toBe(429);
    // 61 seconds is more than one minute, so the reader is told two.
    expect((failure as ApiRequestError).message).toBe("Too many attempts. Try again in 2 minutes.");
  });
});

function sentHeaders(fetchMock: ReturnType<typeof vi.fn>): Headers {
  const init = fetchMock.mock.calls[0]?.[1] as RequestInit | undefined;
  return new Headers(init?.headers);
}

function json(body: unknown, status: number): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
