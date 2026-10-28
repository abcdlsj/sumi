import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { setFeatureEnabled } from "../featureRegistry";
import { SettingsWorkspace } from "./SettingsPage";

beforeEach(() => {
  window.localStorage.clear();
  setFeatureEnabled("agent-insights", false);
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
    expect(screen.getByRole("switch", { name: "Company office" })).toBeVisible();
  });

  it("persists the enable switch per feature", () => {
    render(<SettingsWorkspace />);

    const toggle = screen.getByRole("switch", { name: "Agent insights" });
    expect(toggle).toHaveAttribute("aria-checked", "false");
    fireEvent.click(toggle);

    expect(screen.getByRole("switch", { name: "Agent insights" })).toHaveAttribute("aria-checked", "true");
    expect(window.localStorage.getItem("sumi.feature.agent-insights")).toBe("1");
  });

  it("highlights the feature named in the link", () => {
    render(<SettingsWorkspace selectedFeatureId="company-office" />);

    expect(document.getElementById("feature-company-office")).toHaveClass("settings-row--highlighted");
  });
});
