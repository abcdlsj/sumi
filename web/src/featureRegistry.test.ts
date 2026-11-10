import { beforeEach, describe, expect, it } from "vitest";

import {
  AGENT_INSIGHTS_FEATURE_ID,
  REGISTERED_FEATURES,
  featureEnabled,
  registeredFeature,
  setFeatureEnabled,
} from "./featureRegistry";

beforeEach(() => {
  window.localStorage.clear();
});

describe("feature registry", () => {
  it("registers Agent insights as an experimental feature", () => {
    expect(REGISTERED_FEATURES.map((feature) => feature.id)).toEqual([AGENT_INSIGHTS_FEATURE_ID]);
    expect(registeredFeature(AGENT_INSIGHTS_FEATURE_ID)?.kind).toBe("experimental");
  });

  it("shows an experimental feature until the reader turns it off", () => {
    expect(featureEnabled(AGENT_INSIGHTS_FEATURE_ID)).toBe(true);

    setFeatureEnabled(AGENT_INSIGHTS_FEATURE_ID, false);
    expect(featureEnabled(AGENT_INSIGHTS_FEATURE_ID)).toBe(false);
    expect(window.localStorage.getItem("sumi.feature.agent-insights")).toBe("0");

    setFeatureEnabled(AGENT_INSIGHTS_FEATURE_ID, true);
    expect(featureEnabled(AGENT_INSIGHTS_FEATURE_ID)).toBe(true);
    expect(window.localStorage.getItem("sumi.feature.agent-insights")).toBe("1");
  });

  it("ignores unknown feature ids", () => {
    expect(featureEnabled("missing")).toBe(false);
    setFeatureEnabled("missing", true);
    expect(window.localStorage.getItem("sumi.feature.missing")).toBeNull();
  });
});
