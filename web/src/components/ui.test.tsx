import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { MenuItem } from "./ui";

afterEach(cleanup);

describe("MenuItem", () => {
  it("omits empty icon slot when no icon is provided on radio items", () => {
    const onSelect = vi.fn();
    render(
      <>
        <MenuItem active={true} onSelect={onSelect}>
          Default order
        </MenuItem>
        <MenuItem active={false} onSelect={onSelect}>
          Unread first
        </MenuItem>
      </>,
    );

    const activeItem = screen.getByRole("menuitemradio", { name: "Default order" });
    const inactiveItem = screen.getByRole("menuitemradio", { name: "Unread first" });
    expect(activeItem.getAttribute("aria-checked")).toBe("true");
    expect(inactiveItem.getAttribute("aria-checked")).toBe("false");
    // Only label + check slots (no empty leading icon slot).
    expect(activeItem.children).toHaveLength(2);
    expect(inactiveItem.children).toHaveLength(2);

    fireEvent.click(inactiveItem);
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("omits check slot and uses menuitem role for non-selectable actions", () => {
    render(
      <MenuItem icon={<svg data-testid="ic" />} onSelect={() => {}}>
        Copy link
      </MenuItem>,
    );

    const item = screen.getByRole("menuitem", { name: "Copy link" });
    expect(item.hasAttribute("aria-checked")).toBe(false);
    // Only icon + label slots (no empty trailing check slot).
    expect(item.children).toHaveLength(2);
    expect(screen.getByTestId("ic")).toBeTruthy();
  });
});
