import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import vm from "node:vm";

const root = new URL("../", import.meta.url);
const read = (file) => readFile(new URL(file, root), "utf8");
const manifest = JSON.parse(await read("manifest.json"));

assert.equal(manifest.manifest_version, 3);
assert.equal(manifest.version, "0.1.7");
assert.deepEqual(manifest.host_permissions, [
  "https://telemost.yandex.ru/*",
  "https://dion.vc/*",
  "https://api.dion.vc/*",
  "https://vk.com/*",
  "https://vk.ru/*",
  "http://127.0.0.1/*",
]);
assert.deepEqual([...manifest.permissions].sort(), ["activeTab", "alarms", "cookies", "downloads", "storage", "tabs"]);

const exportContext = { self: {}, URL, Date };
vm.runInNewContext(await read("lib/export-format.js"), exportContext, { filename: "export-format.js" });
const {
  buildBrowserExport,
  parseCookieHeader,
  isTrustedExtensionSender,
  isTrustedPopupSender,
} = exportContext.self.WTBrowserExport;
const pageCookies = parseCookieHeader("L=page-session; yandex_login=operator; yandexuid=device", "telemost.yandex.ru");
assert.deepEqual(Array.from(pageCookies, (cookie) => cookie.name), ["L", "yandex_login", "yandexuid"]);
assert.equal(pageCookies[0].secure, true);
assert.equal(pageCookies[0].httpOnly, false);
const exported = buildBrowserExport({
  cookies: [
    { name: "Session_id", value: "test-only", domain: ".yandex.ru", path: "/", secure: true, httpOnly: true },
    { name: "unrelated", value: "must-not-export", domain: ".yandex.ru", path: "/" },
  ],
  sourceURL: "https://telemost.yandex.ru/j/synthetic-room?invite=must-not-export#private-fragment",
});
assert.equal(exported.source.host, "telemost.yandex.ru");
assert.equal(exported.source.url, "https://telemost.yandex.ru/j/synthetic-room");
assert.deepEqual(Array.from(exported.selectedTypes), ["cookies"]);
assert.deepEqual(Array.from(exported.cookies, (cookie) => cookie.name), ["Session_id"]);
assert.equal(exported.localStorage.length, 0);
assert.equal(Object.hasOwn(exported.cookies[0], "storeId"), false);

const cookiesOnly = buildBrowserExport({
  cookies: [
    { name: "Session_id", value: "test-only", domain: ".yandex.ru", path: "/", storeId: "private-profile-id" },
  ],
  sourceURL: "https://telemost.yandex.ru/j/synthetic-room",
  selectedTypes: ["cookies"],
});
assert.deepEqual(Array.from(cookiesOnly.selectedTypes), ["cookies"]);
assert.equal(cookiesOnly.localStorage.length, 0);
assert.doesNotMatch(JSON.stringify(cookiesOnly), /private-profile-id/);
assert.throws(
  () => buildBrowserExport({ cookies: [], sourceURL: "https://telemost.yandex.ru/", selectedTypes: [] }),
  /Select at least one data type/,
);
assert.throws(
  () => buildBrowserExport({
    cookies: [{ name: "Session_id", value: "x", domain: ".example.com", path: "/" }],
    sourceURL: "https://telemost.yandex.ru/",
    selectedTypes: ["cookies"],
  }),
  /No Yandex Telemost session data/,
);
assert.throws(
  () => buildBrowserExport({ cookies: [], sourceURL: "https://example.com/" }),
  /official HTTPS host/,
);

const extensionID = "abcdefghijklmnopabcdefghijklmnop";
assert.equal(isTrustedExtensionSender({ id: extensionID }, extensionID), true);
assert.equal(isTrustedExtensionSender({ id: "wrong" }, extensionID), false);
assert.equal(
  isTrustedPopupSender({ id: extensionID, url: "chrome-extension://" + extensionID + "/popup.html" }, extensionID),
  true,
);
assert.equal(
  isTrustedPopupSender({ id: extensionID, url: "chrome-extension://" + extensionID + "/content-script.js", tab: { id: 1 } }, extensionID),
  false,
);

const bridgeContext = { self: {} };
vm.runInNewContext(await read("lib/bridge-client.js"), bridgeContext, { filename: "bridge-client.js" });
const bridge = bridgeContext.self.WTBrowserBridge;
const nonce = "a".repeat(64);
const origin = "http://127.0.0.1:17891";
const connection = {origin, nonce};
assert.throws(() => bridge.validateConnection({origin:"https://example.com", nonce}), /Запустите CLI/);
assert.throws(() => bridge.validateConnection({origin,nonce:"demo"}), /Запустите CLI/);
await assert.rejects(() => bridge.sendBrowserExport({}, { fetchImpl: async () => assert.fail("unconfigured fetch") }), /Запустите CLI/);
let request;
const bridgeSummary = await bridge.sendBrowserExport({ version: 1 }, {
  connection,
  fetchImpl: async (url, options) => {
    request = { url, options };
    return {
      ok: true,
      status: 200,
      json: async () => ({ platforms: ["telemost"], count: 1, config_refreshed: true }),
    };
  },
});
assert.equal(JSON.stringify(bridgeSummary), JSON.stringify({ platforms: ["telemost"], count: 1, config_refreshed: true }));
assert.equal(request.url, origin + bridge.BRIDGE_PATH);
assert.equal(request.options.headers["X-WT-Bridge-Nonce"], nonce);
assert.equal(request.options.headers["Content-Type"], "application/json");
assert.equal(JSON.parse(request.options.body).version, 1);
assert.equal(request.options.redirect, "error");
await assert.rejects(
  () => bridge.sendBrowserExport({}, { connection, fetchImpl: async () => ({ ok: false, status: 403 }) }),
  /rejected the export/,
);

const sources = await Promise.all(["content-script.js", "service-worker.js", "popup.js"].map(read));
const bridgeSource = await read("lib/bridge-client.js");
let contentListener;
const contentContext = {
  URL,
  chrome: {
    runtime: {
      id: "abcdefghijklmnopabcdefghijklmnop",
      sendMessage: async () => undefined,
      onMessage: { addListener(fn) { contentListener = fn; } },
    },
  },
  location: {
    protocol: "https:",
    hostname: "telemost.yandex.ru",
    origin: "https://telemost.yandex.ru",
    pathname: "/j/synthetic-room",
  },
  WTBrowserExport: exportContext.self.WTBrowserExport,
  document: { cookie: "L=page-session; yandex_login=operator; yandexuid=device" },
};
vm.runInNewContext(await read("content-script.js"), contentContext, { filename: "content-script.js" });
let contentResponse;
contentListener(
  { type: "wt-read-yandex-telemost-auth", includeCookies: true },
  { id: "abcdefghijklmnopabcdefghijklmnop" },
  (value) => { contentResponse = value; },
);
assert.equal(contentResponse.ok, true);
assert.deepEqual(Array.from(contentResponse.cookies, (cookie) => cookie.name), ["L", "yandex_login", "yandexuid"]);
assert.match(sources[1], /chrome\.downloads\.onChanged\.addListener/);
assert.match(sources[1], /download\.state === "complete"/);
assert.match(sources[1], /periodInMinutes: POLL_PERIOD_MINUTES/);
assert.match(sources[1], /POLL_PERIOD_MINUTES = 0\.5/);
assert.match(sources[1], /wt-set-yandex-telemost-polling/);
assert.doesNotMatch(sources[1], /attempt < 20/);
assert.doesNotMatch(sources.join("\n"), /XMLHttpRequest|WebSocket/);
assert.match(sources[1], /WTBrowserBridge\.sendBrowserExport/);
assert.match(bridgeSource, /requestFetch/);

const popupHTML = await read("popup.html");
assert.match(popupHTML, /id="include-cookies"/);
assert.match(popupHTML, /id="save-json"/);
assert.match(popupHTML, /id="import-command"/);
assert.match(popupHTML, /данные входа/i);
assert.match(popupHTML, /Cookies текущего сервиса/i);
assert.doesNotMatch(popupHTML, /id="pair-line"|READY/);
assert.match(popupHTML, /id="copy-meeting"/);
console.log("browser-extension validation passed");
