import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const read = (f: string) => JSON.parse(readFileSync(new URL(f, import.meta.url), "utf8"));
const manifest = read("./manifest.json");
const firefox = read("./manifest.firefox.json");
const pkg = read("../package.json");

describe("manifest", () => {
  it("the Chrome manifest has a service worker and nothing Chrome would warn about", () => {
    expect(manifest.background).toEqual({ service_worker: "background.js", type: "module" });
    expect(manifest.browser_specific_settings).toBeUndefined();
  });

  it("the Firefox additions are kept apart: background scripts and the add-on id", () => {
    expect(firefox.background.scripts).toEqual(["background.js"]);
    expect(firefox.browser_specific_settings.gecko.id).toBeTruthy();
    expect(Object.keys(firefox).sort()).toEqual(["background", "browser_specific_settings"]);
  });

  it("injects window.webln only into https pages and local development hosts", () => {
    for (const cs of manifest.content_scripts) {
      expect(cs.matches).toEqual(["https://*/*", "http://localhost/*", "http://127.0.0.1/*"]);
    }
  });

  it("has a description of at most 132 characters, the Chrome Web Store limit", () => {
    expect(manifest.description.length).toBeLessThanOrEqual(132);
  });

  it("has the same version as package.json", () => {
    expect(manifest.version).toBe(pkg.version);
  });

  it("allows https connections (Lightning addresses) but no remote scripts", () => {
    const csp: string = manifest.content_security_policy.extension_pages;
    expect(csp).toMatch(/script-src 'self' 'wasm-unsafe-eval'/);
    expect(csp).toMatch(/connect-src 'self' https:/);
  });
});
