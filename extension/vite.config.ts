import { defineConfig, type Plugin } from "vite";
import { resolve } from "node:path";
import { build as esbuild } from "esbuild";

function flattenExtensionHtml(): Plugin {
  return {
    name: "flatten-extension-html",
    generateBundle(_opts, bundle) {
      for (const [fileName, item] of Object.entries(bundle)) {
        if (item.type !== "asset" || !fileName.endsWith(".html")) continue;
        const base = fileName.includes("offscreen") ? "offscreen.html" : "index.html";
        let src = String(item.source);
        src = src
          .replace(/(src|href)="[^"]*?popup\.js"/g, '$1="popup.js"')
          .replace(/(src|href)="[^"]*?popup\.css"/g, '$1="popup.css"')
          .replace(/(src|href)="[^"]*?offscreen\.js"/g, '$1="offscreen.js"');
        if (fileName !== base) delete bundle[fileName];
        bundle[base] = { ...item, fileName: base, source: src };
      }
    },
  };
}

function buildContentScripts(): Plugin {
  return {
    name: "build-content-scripts",
    async closeBundle() {
      const outdir = resolve(__dirname, "dist");
      for (const [name, file] of [
        ["content", "src/content.ts"],
        ["inject", "src/inject-webln.ts"],
      ] as const) {
        await esbuild({
          entryPoints: [resolve(__dirname, file)],
          outfile: resolve(outdir, `${name}.js`),
          bundle: true,
          format: "iife",
          platform: "browser",
          target: "es2022",
          minify: false,
        });
      }
    },
  };
}

export default defineConfig({
  base: "./",
  publicDir: false,
  plugins: [flattenExtensionHtml(), buildContentScripts()],
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    minify: false,
    target: "es2022",
    modulePreload: false,
    rollupOptions: {
      input: {
        background: resolve(__dirname, "src/background.ts"),
        offscreen: resolve(__dirname, "src/offscreen.html"),
        popup: resolve(__dirname, "src/popup/index.html"),
      },
      output: {
        entryFileNames: "[name].js",
        chunkFileNames: "chunks/[name].js",
        assetFileNames: "[name][extname]",
        format: "es",
      },
    },
  },
});
