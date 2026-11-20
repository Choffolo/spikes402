import { cpSync, mkdirSync, existsSync, readFileSync, writeFileSync, rmSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const dist = resolve(root, "dist");
const sdkSrc = resolve(root, "node_modules/@breeztech/breez-sdk-spark/web");
const sdkDest = resolve(dist, "vendor/breez-sdk");

if (!existsSync(sdkSrc)) {
  throw new Error("Breez SDK web build is missing. Run npm install in extension/ first.");
}

mkdirSync(dist, { recursive: true });
cpSync(resolve(root, "src/manifest.json"), resolve(dist, "manifest.json"));
cpSync(resolve(root, "icons"), resolve(dist, "icons"), { recursive: true });
cpSync(sdkSrc, sdkDest, { recursive: true });

function flattenHtml(from, to) {
  if (!existsSync(from)) throw new Error(`Expected ${from} after the Vite build.`);
  let html = readFileSync(from, "utf8");
  html = html
    .replaceAll(' crossorigin', "")
    .replace(/(src|href)="\/([^"]+)"/g, '$1="$2"')
    .replace(/(src|href)="(?:\.\.\/)+([^"]+)"/g, '$1="$2"');
  writeFileSync(to, html);
}

flattenHtml(resolve(dist, "src/popup/index.html"), resolve(dist, "index.html"));
flattenHtml(resolve(dist, "src/offscreen.html"), resolve(dist, "offscreen.html"));
rmSync(resolve(dist, "src"), { recursive: true, force: true });

console.log("Copied manifest, icons, and the local Breez SDK (WASM) into dist/.");
