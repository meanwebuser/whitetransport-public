import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";

const root = new URL("../", import.meta.url);
const read = (file) => readFile(new URL(file, root), "utf8");
const manifest = JSON.parse(await read("manifest.json"));

assert.equal(manifest.manifest_version, 3);
assert.equal(manifest.version, "0.1.0");
assert.deepEqual(manifest.permissions, ["activeTab"]);
assert.equal(manifest.host_permissions, undefined);
assert.equal(manifest.background, undefined);
assert.equal(manifest.content_scripts, undefined);

const popupSource = await read("popup.js");
assert.doesNotMatch(popupSource, /chrome\.cookies|chrome\.storage|chrome\.tabs\.create|chrome\.downloads|XMLHttpRequest|fetch\(|WebSocket|localStorage|sessionStorage|document\.cookie/);
assert.match(popupSource, /chrome\.tabs\.query/);
assert.match(popupSource, /navigator\.clipboard\.writeText/);

// Reuse the same meetingURL logic by loading popup.js in a stubbed environment.
const status = { className: "", textContent: "" };
const clicked = [];
globalThis.chrome = {
  tabs: {
    query: async () => [{ url: clicked.shift() || "https://example.invalid/" }],
  },
};
Object.defineProperty(globalThis, "navigator", {
  value: { clipboard: { writeText: async (v) => { globalThis.__clipboard = v; } } },
  configurable: true,
});
globalThis.document = {
  querySelector: (sel) => sel === "#copy"
    ? { addEventListener: (_evt, fn) => { globalThis.__handler = fn; } }
    : status,
};

// Load popup.js once per test case so each case starts with fresh state.
function loadPopup(src) {
  // eslint-disable-next-line no-eval
  (0, eval)(src);
}

const cases = [
  ["https://telemost.yandex.ru/j/abc123", "https://telemost.yandex.ru/j/abc123", true],
  ["https://telemost.yandex.ru/j/abc123?invite=omit#frag", "https://telemost.yandex.ru/j/abc123", true],
  ["https://telemost.yandex.ru/j/abc123/deeper", null, false],
  ["https://telemost.yandex.ru/j/", null, false],
  ["https://telemost.yandex.ru/", null, false],
  ["https://telemost.yandex.ru/chats", null, false],
  ["https://telemost.yandex.ru.example.com/j/abc123", null, false],
  ["https://telemost.evil.com/j/abc123", null, false],
  ["http://telemost.yandex.ru/j/abc123", null, false],
  ["https://user:pass@telemost.yandex.ru/j/abc123", null, false],
];

for (const [input, expected, shouldCopy] of cases) {
  clicked.push(input);
  const src = await read("popup.js");
  loadPopup(src);
  const handler = globalThis.__handler;
  assert.equal(typeof handler, "function", "copy handler was not registered");
  await handler();
  status.textContent = "";
  // The click handler is async; give the event loop one turn.
  await new Promise((resolve) => setTimeout(resolve, 20));
  const copied = globalThis.__clipboard;
  assert.equal(shouldCopy ? copied : undefined, shouldCopy ? expected : undefined, "case: " + input);
  if (shouldCopy) {
    assert.equal(copied, expected);
    assert.match(status.className, /ok/);
  } else {
    assert.match(status.className, /err/);
  }
  delete globalThis.__clipboard;
}

console.log("validate.mjs OK");
