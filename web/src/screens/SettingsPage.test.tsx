import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { SettingsWorkspace } from "./SettingsPage";

beforeEach(() => {
  window.localStorage.clear();
});

afterEach(() => {
  cleanup();
});

describe("SettingsWorkspace", () => {
  it("lists every registered feature on one page", () => {
    render(<SettingsWorkspace />);

    expect(screen.getByRole("heading", { name: "Settings" })).toBeVisible();
    expect(screen.getByRole("heading", { name: "Experimental features" })).toBeVisible();
    expect(screen.getByRole("switch", { name: "Agent insights" })).toBeVisible();
  });

  it("starts with the experimental feature shown and persists a turn-off", () => {
    render(<SettingsWorkspace />);

    const toggle = screen.getByRole("switch", { name: "Agent insights" });
    expect(toggle).toHaveAttribute("aria-checked", "true");
    fireEvent.click(toggle);

    expect(screen.getByRole("switch", { name: "Agent insights" })).toHaveAttribute("aria-checked", "false");
    expect(window.localStorage.getItem("sumi.feature.agent-insights")).toBe("0");
  });

  it("highlights the feature named in the link", () => {
    render(<SettingsWorkspace selectedFeatureId="agent-insights" />);

    expect(document.getElementById("feature-agent-insights")).toHaveClass("settings-row--highlighted");
  });
});
