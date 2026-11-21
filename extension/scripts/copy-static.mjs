import { cpSync, mkdirSync, existsSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const dist = resolve(root, "dist");

mkdirSync(dist, { recursive: true });
// Chrome gets the plain manifest. Firefox needs a different "background" key
// and its add-on id, which Chrome would report as warnings, so they live in
// src/manifest.firefox.json and are merged in only for `npm run build:firefox`.
const manifest = JSON.parse(readFileSync(resolve(root, "src/manifest.json"), "utf8"));
if (process.argv.includes("--firefox")) {
  Object.assign(manifest, JSON.parse(readFileSync(resolve(root, "src/manifest.firefox.json"), "utf8")));
}
writeFileSync(resolve(dist, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n");
cpSync(resolve(root, "icons"), resolve(dist, "icons"), { recursive: true });

function flattenHtml(from, to) {
  if (!existsSync(from)) throw new Error(`Expected ${from} after the Vite build.`);
  let html = readFileSync(from, "utf8");
  html = html
    .replaceAll(" crossorigin", "")
    .replace(/(src|href)="\/([^"]+)"/g, '$1="$2"')
    .replace(/(src|href)="(?:\.\.\/)+([^"]+)"/g, '$1="$2"');
  writeFileSync(to, html);
}

flattenHtml(resolve(dist, "src/popup/index.html"), resolve(dist, "index.html"));
flattenHtml(resolve(dist, "src/offscreen.html"), resolve(dist, "offscreen.html"));
rmSync(resolve(dist, "src"), { recursive: true, force: true });

console.log(`Copied the ${process.argv.includes("--firefox") ? "Firefox" : "Chrome"} manifest and icons into dist/.`);
