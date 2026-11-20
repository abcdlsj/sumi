import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { NewMessagesPill, useStickToBottom } from "./scroll";

function Harness({ streamKey, count, anchor }: { streamKey: string; count: number; anchor?: boolean }) {
  const stick = useStickToBottom({ streamKey, count, anchorId: anchor ? "new" : undefined });
  return (
    <div>
      <div ref={stick.ref} onScroll={stick.onScroll} data-testid="stream">
        {Array.from({ length: count }).map((_, i) => (
          <div key={i} data-unread-anchor={anchor && i === 1 ? "new" : undefined} style={{ height: 40 }}>
            message {i}
          </div>
        ))}
      </div>
      <NewMessagesPill count={stick.newCount} onClick={stick.jumpToEnd} />
    </div>
  );
}

/** Sizes the (otherwise layout-free) jsdom box. */
function box(el: HTMLElement, scrollHeight: number, clientHeight = 100) {
  Object.defineProperty(el, "scrollHeight", { value: scrollHeight, configurable: true });
  Object.defineProperty(el, "clientHeight", { value: clientHeight, configurable: true });
}

/** Mounts an empty stream and sizes it, as the view does before messages load. */
function open() {
  const view = render(<Harness streamKey="c1" count={0} />);
  const el = screen.getByTestId("stream");
  box(el, 0);
  return { ...view, el };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useStickToBottom", () => {
  it("lands on the end of a new stream and follows later arrivals", () => {
    const view = open();
    box(view.el, 600);
    view.rerender(<Harness streamKey="c1" count={3} />);
    expect(view.el.scrollTop).toBe(600);

    box(view.el, 700);
    view.rerender(<Harness streamKey="c1" count={4} />);
    expect(view.el.scrollTop).toBe(700);
    expect(screen.queryByRole("button", { name: /new message/ })).toBeNull();
  });

  it("parks the stream and counts arrivals when the reader scrolled up", () => {
    const view = open();
    box(view.el, 600);
    view.rerender(<Harness streamKey="c1" count={3} />);

    view.el.scrollTop = 100;
    fireEvent.scroll(view.el);
    box(view.el, 700);
    view.rerender(<Harness streamKey="c1" count={4} />);
    box(view.el, 780);
    view.rerender(<Harness streamKey="c1" count={5} />);

    expect(view.el.scrollTop).toBe(100);
    fireEvent.click(screen.getByRole("button", { name: /2 new messages/ }));
    expect(view.el.scrollTop).toBe(780);
    expect(screen.queryByRole("button", { name: /new message/ })).toBeNull();
  });

  it("clears the counter when the reader scrolls back to the end", () => {
    const view = open();
    box(view.el, 600);
    view.rerender(<Harness streamKey="c1" count={3} />);

    view.el.scrollTop = 100;
    fireEvent.scroll(view.el);
    box(view.el, 700);
    view.rerender(<Harness streamKey="c1" count={4} />);
    expect(screen.getByRole("button", { name: /1 new message/ })).toBeTruthy();

    view.el.scrollTop = 600;
    fireEvent.scroll(view.el);
    expect(screen.queryByRole("button", { name: /new message/ })).toBeNull();
  });

  it("opens an unread stream on its divider instead of the end", () => {
    const view = open();
    box(view.el, 600);
    view.rerender(<Harness streamKey="c1" count={5} anchor />);
    expect(view.el.scrollTop).toBe(0);
  });

  it("re-anchors when the divider only arrives with the fresh page", () => {
    const view = open();
    box(view.el, 600);
    view.rerender(<Harness streamKey="c1" count={2} />);
    expect(view.el.scrollTop).toBe(600);

    box(view.el, 900);
    view.rerender(<Harness streamKey="c1" count={6} anchor />);
    // jsdom reports zero geometry, so the divider lands one padding step above the old position.
    expect(view.el.scrollTop).toBe(584);
  });

  it("keeps the end in view when content grows after render", () => {
    let fire: (() => void) | null = null;
    vi.stubGlobal(
      "ResizeObserver",
      class {
        constructor(cb: () => void) {
          fire = cb;
        }
        observe() {}
        disconnect() {}
      },
    );
    const view = open();
    box(view.el, 600);
    view.rerender(<Harness streamKey="c1" count={3} />);
    view.el.scrollTop = 0;
    act(() => fire?.());
    expect(view.el.scrollTop).toBe(600);
  });
});
