import { cp, mkdir, readFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const nativeGuiDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const sourceDir = path.resolve(nativeGuiDir, "../client-web/dist");
const destinationDir = path.join(nativeGuiDir, "frontend/dist");
const markerName = "whitetransport-client-web.json";
const expectedMarker = {
  bundle: "@whitetransport/client-web",
  schema: 1,
  shell: ["home", "endpoints", "settings"],
};

const sourceMarker = JSON.parse(await readFile(path.join(sourceDir, markerName), "utf8"));
if (JSON.stringify(sourceMarker) !== JSON.stringify(expectedMarker)) {
  throw new Error("Refusing to package an unknown client-web bundle");
}

await rm(destinationDir, { recursive: true, force: true });
await mkdir(path.dirname(destinationDir), { recursive: true });
await cp(sourceDir, destinationDir, { recursive: true });
await readFile(path.join(destinationDir, "index.html"), "utf8");
