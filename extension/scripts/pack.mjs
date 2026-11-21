import { existsSync, rmSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";

const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const dist = resolve(root, "dist");
const zip = resolve(root, "spikes402-extension.zip");

if (!existsSync(resolve(dist, "manifest.json"))) {
  throw new Error("dist/ is missing a manifest. Run npm run build first.");
}

if (existsSync(zip)) rmSync(zip);

const script = `
import zipfile, os, sys
root, out = sys.argv[1], sys.argv[2]
with zipfile.ZipFile(out, "w", compression=zipfile.ZIP_DEFLATED, compresslevel=6) as z:
    for dirpath, dirnames, filenames in os.walk(root):
        for name in filenames:
            path = os.path.join(dirpath, name)
            z.write(path, os.path.relpath(path, root))
print("Wrote", out, "(%d bytes)" % os.path.getsize(out))
`
const r = spawnSync("python3", ["-c", script, dist, zip], { stdio: "inherit" });
if (r.status !== 0) throw new Error("zip failed");
