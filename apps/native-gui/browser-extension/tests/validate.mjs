import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";

const root = new URL("../", import.meta.url);
const read = (file) => readFile(new URL(file, root), "utf8");
const manifest = JSON.parse(await read("manifest.json"));

assert.equal(manifest.manifest_version, 3);
assert.deepEqual(manifest.host_permissions, ["https://storage-control-stream.wb.ru/*"]);
assert.deepEqual([...manifest.permissions].sort(), ["activeTab", "cookies", "downloads"]);

const context = { self: {}, URL, Date };
vm.runInNewContext(await read("lib/export-format.js"), context, { filename: "export-format.js" });
const {
  buildBrowserExport,
  isTrustedExtensionSender,
  isTrustedPopupSender,
} = context.self.WTBrowserExport;
const exported = buildBrowserExport({
  cookies: [
    { name: "x_wbaas_token", value: "test-only", domain: ".wb.ru", path: "/", secure: true, httpOnly: true },
    { name: "unrelated", value: "must-not-export", domain: ".wb.ru", path: "/" },
  ],
  authSlice: '{"accessToken":"test-only"}',
  sourceURL: "https://storage-control-stream.wb.ru/room?invite=must-not-export#private-fragment",
});
assert.equal(exported.source.host, "storage-control-stream.wb.ru");
assert.equal(exported.source.url, "https://storage-control-stream.wb.ru/");
assert.deepEqual(Array.from(exported.selectedTypes), ["cookies", "localStorage"]);
assert.deepEqual(Array.from(exported.cookies, (cookie) => cookie.name), ["x_wbaas_token"]);
assert.deepEqual(Array.from(exported.localStorage, (entry) => entry.key), ["wb_auth_auth_slice"]);
assert.equal(Object.hasOwn(exported.cookies[0], "storeId"), false);

const cookiesOnly = buildBrowserExport({
  cookies: [
    { name: "x_wbaas_token", value: "test-only", domain: ".wb.ru", path: "/", storeId: "private-profile-id" },
  ],
  authSlice: '{"accessToken":"must-not-export"}',
  sourceURL: "https://storage-control-stream.wb.ru/",
  selectedTypes: ["cookies"],
});
assert.deepEqual(Array.from(cookiesOnly.selectedTypes), ["cookies"]);
assert.equal(cookiesOnly.localStorage.length, 0);
assert.doesNotMatch(JSON.stringify(cookiesOnly), /private-profile-id|must-not-export/);
assert.throws(
  () => buildBrowserExport({ cookies: [], authSlice: "x", sourceURL: "https://storage-control-stream.wb.ru/", selectedTypes: [] }),
  /Select at least one data type/,
);
assert.throws(
  () => buildBrowserExport({
    cookies: [{ name: "x_wbaas_token", value: "x", domain: ".example.com", path: "/" }],
    authSlice: null,
    sourceURL: "https://storage-control-stream.wb.ru/",
    selectedTypes: ["cookies"],
  }),
  /No WBStream session data/,
);
assert.throws(
  () => buildBrowserExport({ cookies: [], authSlice: "x", sourceURL: "https://example.com/" }),
  /official HTTPS host/,
);

const extensionID = "abcdefghijklmnopabcdefghijklmnop";
assert.equal(isTrustedExtensionSender({ id: extensionID }, extensionID), true);
assert.equal(isTrustedExtensionSender({ id: "wrong" }, extensionID), false);
assert.equal(
  isTrustedPopupSender({ id: extensionID, url: `chrome-extension://${extensionID}/popup.html` }, extensionID),
  true,
);
assert.equal(
  isTrustedPopupSender({ id: extensionID, url: `chrome-extension://${extensionID}/content-script.js`, tab: { id: 1 } }, extensionID),
  false,
);

const sources = await Promise.all(["content-script.js", "service-worker.js", "popup.js"].map(read));
assert.doesNotMatch(sources.join("\n"), /\b(fetch|XMLHttpRequest|WebSocket)\b/);
assert.match(sources[0], /isTrustedExtensionSender/);
assert.match(sources[1], /isTrustedPopupSender/);

const popupHTML = await read("popup.html");
assert.match(popupHTML, /id="include-cookies"/);
assert.match(popupHTML, /id="include-local-storage"/);
assert.match(popupHTML, /файл содержит данные входа/i);
console.log("browser-extension validation passed");
