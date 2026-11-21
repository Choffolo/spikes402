import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

const manifest = JSON.parse(readFileSync(new URL("./manifest.json", import.meta.url), "utf8"));
const pkg = JSON.parse(readFileSync(new URL("../package.json", import.meta.url), "utf8"));

describe("manifest", () => {
  it("has a service worker for Chrome and background scripts for Firefox", () => {
    expect(manifest.background.service_worker).toBe("background.js");
    expect(manifest.background.scripts).toEqual(["background.js"]);
  });

  it("has the same version as package.json", () => {
    expect(manifest.version).toBe(pkg.version);
  });
});
