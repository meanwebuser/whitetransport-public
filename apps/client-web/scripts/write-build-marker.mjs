import { mkdir, writeFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const appDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const distDir = path.join(appDir, "dist");
const markerPath = path.join(distDir, "whitetransport-client-web.json");
const marker = {
  bundle: "@whitetransport/client-web",
  schema: 1,
  shell: ["home", "endpoints", "settings"],
};

await mkdir(distDir, { recursive: true });
await writeFile(markerPath, `${JSON.stringify(marker)}\n`, "utf8");
