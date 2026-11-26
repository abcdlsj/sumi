import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { clearPreferencesForTests, writeLongMessagesPref } from "../preferences";
import { clearCollapsibleOverridesForTests, CollapsibleMessage } from "./CollapsibleMessage";

let lastObserverCallback: ResizeObserverCallback | null = null;
let observedTargets: Element[] = [];

class MockResizeObserver {
  constructor(cb: ResizeObserverCallback) {
    lastObserverCallback = cb;
  }
  observe(target: Element) {
    observedTargets.push(target);
  }
  unobserve(target: Element) {
    observedTargets = observedTargets.filter((t) => t !== target);
  }
  disconnect() {
    observedTargets = [];
  }
}

function triggerResize(height: number) {
  const target = observedTargets[observedTargets.length - 1];
  if (!target || !lastObserverCallback) throw new Error("no observed target");
  act(() => {
    lastObserverCallback!(
      [
        {
          target,
          borderBoxSize: [{ blockSize: height, inlineSize: 400 }],
          contentRect: { height } as DOMRectReadOnly,
        } as unknown as ResizeObserverEntry,
      ],
      {} as ResizeObserver,
    );
  });
}

describe("CollapsibleMessage", () => {
  const origRO = globalThis.ResizeObserver;

  beforeEach(() => {
    clearPreferencesForTests();
    clearCollapsibleOverridesForTests();
    observedTargets = [];
    lastObserverCallback = null;
    globalThis.ResizeObserver = MockResizeObserver as unknown as typeof ResizeObserver;
  });

  afterEach(() => {
    globalThis.ResizeObserver = origRO;
    clearPreferencesForTests();
    clearCollapsibleOverridesForTests();
  });

  it("renders tall messages expanded by default and collapses on clicking Show less", () => {
    const { container, unmount } = render(
      <CollapsibleMessage messageID="msg-1">
        <div>Long content</div>
      </CollapsibleMessage>,
    );

    triggerResize(500);

    const wrapper = container.firstElementChild as HTMLElement;
    expect(wrapper.dataset.collapsible).toBe("true");
    expect(wrapper.dataset.collapsed).toBe("false");
    const showLess = screen.getByRole("button", { name: "Show less" });
    expect(showLess).toBeTruthy();

    fireEvent.click(showLess);
    expect(wrapper.dataset.collapsed).toBe("true");
    expect(screen.getByRole("button", { name: "Show more" })).toBeTruthy();

    // Remembers manual collapse across remounts.
    unmount();
    const second = render(
      <CollapsibleMessage messageID="msg-1">
        <div>Long content</div>
      </CollapsibleMessage>,
    );
    triggerResize(500);
    expect((second.container.firstElementChild as HTMLElement).dataset.collapsed).toBe("true");
    expect(screen.getByRole("button", { name: "Show more" })).toBeTruthy();
  });

  it("starts collapsed when the long messages preference is set to collapsed", () => {
    writeLongMessagesPref("collapsed");

    const { container } = render(
      <CollapsibleMessage messageID="msg-2">
        <div>Long content</div>
      </CollapsibleMessage>,
    );

    triggerResize(500);

    const wrapper = container.firstElementChild as HTMLElement;
    expect(wrapper.dataset.collapsed).toBe("true");
    const showMore = screen.getByRole("button", { name: "Show more" });
    fireEvent.click(showMore);
    expect(wrapper.dataset.collapsed).toBe("false");
    expect(screen.getByRole("button", { name: "Show less" })).toBeTruthy();
  });

  it("resets per-message toggle overrides when the global preference changes", () => {
    const { container } = render(
      <CollapsibleMessage messageID="msg-3">
        <div>Long content</div>
      </CollapsibleMessage>,
    );
    triggerResize(500);

    // Manually collapse while default is expanded.
    fireEvent.click(screen.getByRole("button", { name: "Show less" }));
    expect((container.firstElementChild as HTMLElement).dataset.collapsed).toBe("true");

    // Switching preference to collapsed and back to expanded resets the override.
    act(() => {
      writeLongMessagesPref("collapsed");
    });
    expect((container.firstElementChild as HTMLElement).dataset.collapsed).toBe("true");

    act(() => {
      writeLongMessagesPref("expanded");
    });
    expect((container.firstElementChild as HTMLElement).dataset.collapsed).toBe("false");
  });

  it("omits the toggle button for short messages", () => {
    const { container } = render(
      <CollapsibleMessage messageID="msg-short">
        <div>Short content</div>
      </CollapsibleMessage>,
    );
    triggerResize(120);
    expect((container.firstElementChild as HTMLElement).dataset.collapsible).toBe("false");
    expect(screen.queryByRole("button")).toBeNull();
  });
});
