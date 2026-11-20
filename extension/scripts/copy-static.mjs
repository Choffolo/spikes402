import { cpSync, mkdirSync, existsSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const dist = resolve(root, "dist");

mkdirSync(dist, { recursive: true });
cpSync(resolve(root, "src/manifest.json"), resolve(dist, "manifest.json"));
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

console.log("Copied manifest and icons into dist/.");
