import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { HumanAvatar, StatusDot } from "./Avatar";

describe("HumanAvatar", () => {
  it("shows one character: first Latin letter upper-cased, first CJK character kept", () => {
    const { rerender } = render(<HumanAvatar seed="m-1" name="owner" />);
    expect(screen.getByRole("img", { name: "owner" }).textContent).toBe("O");
    rerender(<HumanAvatar seed="m-2" name="林薇" />);
    expect(screen.getByRole("img", { name: "林薇" }).textContent).toBe("林");
    const { container } = render(<HumanAvatar seed="m-3" name=" " />);
    expect(container.firstElementChild!.textContent).toBe("?");
  });

  it("derives the tone from the member id, not the display name", () => {
    const { container, rerender } = render(<HumanAvatar seed="member-1" name="owner" />);
    const tone = container.firstElementChild!.className;
    expect(tone).toMatch(/avatar-tone-[1-8]/);
    rerender(<HumanAvatar seed="member-1" name="renamed person" />);
    expect(container.firstElementChild!.className).toBe(tone);
  });
});

describe("StatusDot", () => {
  it("fills each runtime state from its theme token", () => {
    const { container } = render(
      <>
        <StatusDot state="idle" />
        <StatusDot state="working" />
        <StatusDot state="error" />
        <StatusDot state="offline" />
      </>,
    );
    const [idle, working, error, offline] = [...container.children] as HTMLElement[];
    expect(idle.style.background).toBe("var(--online)");
    expect(working.style.background).toBe("var(--busy)");
    expect(working.className).toContain("animate-breathe");
    expect(error.style.background).toBe("var(--danger)");
    expect(offline.style.background).toBe("transparent");
  });
});
