/* global chrome, importScripts, WTBrowserBridge, WTBrowserExport */

"use strict";

importScripts("lib/export-format.js", "lib/bridge-client.js");

const POLL_ALARM_NAME = "wt-yandex-telemost-refresh";
const POLL_PERIOD_MINUTES = 0.5;
const POLL_STORAGE_KEY = "yandexTelemostPolling";
const PROVIDER_HOSTS = ["telemost.yandex.ru", "dion.vc", "vk.com", "vk.ru"];
const DION_AUTH_URL = "https://api.dion.vc/platform/v1/auth/refresh/web";
const DION_NAMES = ["vc-access-token", "vc-refresh-token"];

function providerURL(raw) {
  const url = new URL(raw);
  if (url.protocol !== "https:" || url.port || url.username || url.password || !PROVIDER_HOSTS.includes(url.hostname)) {
    throw new Error("Откройте вкладку Телемоста, DION или VK.");
  }
  return url;
}

async function activeBrowserTab() {
  const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (!tab?.id || !tab.url) {
    throw new Error("Open the authenticated HTTPS service page first");
  }

  providerURL(tab.url);
  return tab;
}

async function waitForBrowserTab(tabId) {
  return new Promise((resolve, reject) => {
		let settled = false;
    const timeout = setTimeout(() => {
			finish(reject, new Error("Timed out waiting for the service page"));
    }, 15000);
		const finish = (callback, value) => {
			if (settled) {
				return;
			}
			settled = true;
			clearTimeout(timeout);
			chrome.tabs.onUpdated.removeListener(onUpdated);
			callback(value);
		};
    function onUpdated(updatedTabId, changeInfo, tab) {
      if (updatedTabId !== tabId || changeInfo.status !== "complete") {
        return;
      }
			finish(resolve, tab);
    }
    chrome.tabs.onUpdated.addListener(onUpdated);
		chrome.tabs.get(tabId).then((current) => {
			if (current?.status === "complete") {
				finish(resolve, current);
			}
		}).catch((error) => finish(reject, error));
  });
}

async function pollingBrowserTab(command = {}) {
  let requestedURL = "";
  if (typeof command.url === "string" && command.url.trim()) {
    requestedURL = providerURL(command.url).href;
  }

  const tabs = await chrome.tabs.query({ url: PROVIDER_HOSTS.map(host => "https://" + host + "/*") });
  if (requestedURL) {
    const requested = new URL(requestedURL);
    const existing = tabs.find((tab) => {
      try {
        const current = new URL(tab.url);
        return current.origin === requested.origin &&
          (requested.href === requested.origin + "/" || current.href === requested.href);
      } catch {
        return false;
      }
    });
    if (existing?.id) {
      return waitForBrowserTab(existing.id);
    }
    const opened = await chrome.tabs.create({ url: requestedURL, active: false });
    if (!opened?.id) {
      throw new Error("Could not open the refresh command URL");
    }
    return waitForBrowserTab(opened.id);
  }

  const tab = tabs.find((candidate) => candidate.active) || tabs[0];
  if (!tab?.id || !tab.url) {
    throw new Error("No HTTPS service tab is open");
  }
  providerURL(tab.url);
  return tab;
}

async function selectedCookies(includeCookies) {
  if (!includeCookies) {
    return [];
  }
  const cookiesByName = await Promise.all(
    WTBrowserExport.YANDEX_COOKIE_NAMES.map((name) =>
      chrome.cookies.getAll({ url: "https://" + WTBrowserExport.YANDEX_HOST + "/", name }),
    ),
  );
  return cookiesByName.flat();
}

async function selectedCookiesForTab(includeCookies, tab) {
  if (!includeCookies) {
    return [];
  }
  const url = providerURL(tab.url);
  if (url.hostname === WTBrowserExport.YANDEX_HOST) {
    return selectedCookies(true);
  }
  if (url.hostname === "dion.vc") {
    const store = (await chrome.cookies.getAllCookieStores()).find(item => item.tabIds.includes(tab.id));
    if (!store) throw new Error("Хранилище сессии DION недоступно.");
    return (await Promise.all(DION_NAMES.map(async name => {
      const query = { name, storeId: store.id };
      const scoped = { ...query, domain: "dion.vc", secure: true };
      const [api, page, all, partitioned] = await Promise.all([
        chrome.cookies.get({ ...query, url: DION_AUTH_URL }),
        chrome.cookies.get({ ...query, url: tab.url }),
        chrome.cookies.getAll(scoped),
        chrome.cookies.getAll({ ...scoped, partitionKey: { topLevelSite: url.origin } }),
      ]);
      return [api, page, ...all, ...partitioned].filter(cookie => {
        const domain = cookie?.domain?.replace(/^\./, "");
        return cookie?.value && (domain === "dion.vc" || domain === "api.dion.vc");
      });
    }))).flat();
  }
  return chrome.cookies.getAll({ url: tab.url, name: "remixsid" });
}

function dionClaims(cookie) {
  try {
    const payload = cookie.value.split(".")[1].replace(/-/g, "+").replace(/_/g, "/");
    return JSON.parse(atob(payload));
  } catch {
    return {};
  }
}

function dionSessionCookies(cookies) {
  const candidates = name => [...new Map(cookies.filter(cookie => cookie.name === name)
    .map(cookie => [cookie.value, cookie])).values()];
  const [accesses, refreshes] = DION_NAMES.map(candidates);
  if (!accesses.length) throw new Error("DION: cookie доступа отсутствует. Войдите в DION.");
  if (!refreshes.length) throw new Error("DION: cookie обновления отсутствует. Нужен новый вход в DION.");

  const pairs = refreshes.flatMap(refresh => {
    const r = dionClaims(refresh);
    const matching = accesses.filter(access => {
      const a = dionClaims(access);
      return a.user_id != null && a.sid != null && a.user_id === r.user_id && a.sid === r.sid;
    });
    return matching.length ? [[matching[0], refresh]] : [];
  });
  if (pairs.length === 1) return pairs[0];
  if (accesses.length === 1 && refreshes.length === 1 &&
      dionClaims(accesses[0]).sid == null && dionClaims(refreshes[0]).sid == null) {
    return [accesses[0], refreshes[0]];
  }
  throw new Error("DION: Chrome вернул несовместимые или несколько сессий.");
}

function mergeCookies(...groups) {
  const seen = new Set();
  return groups.flat().filter((cookie) => {
    if (!cookie || typeof cookie.name !== "string") {
      return false;
    }
    const key = [cookie.name, cookie.domain || "", cookie.path || "/", cookie.value || ""].join("|");
    if (seen.has(key)) {
      return false;
    }
    seen.add(key);
    return true;
  });
}

async function createExport(selection, { polling = false, command = {} } = {}) {
  const tab = polling ? await pollingBrowserTab(command) : await activeBrowserTab();
  const url = providerURL(tab.url);
  if (command.url && providerURL(command.url).origin !== url.origin) {
    throw new Error("Сервис перенаправил вкладку на другой сайт.");
  }
  const response = await chrome.tabs.sendMessage(tab.id, {
    type: "wt-read-browser-auth",
    includeCookies: selection.cookies,
  });
  if (!response?.ok) {
    throw new Error(response?.error || "Could not read this Yandex Telemost tab");
  }

  if (providerURL(response.sourceURL).origin !== url.origin) {
    throw new Error("Адрес вкладки изменился во время чтения сессии.");
  }
  const selected = await selectedCookiesForTab(selection.cookies, tab);
  const cookies = mergeCookies(selected, response.cookies || []);
  const exportBuilder = url.hostname === WTBrowserExport.YANDEX_HOST
    ? WTBrowserExport.buildBrowserExport : WTBrowserExport.buildGenericBrowserExport;
  const filtered = url.hostname === "dion.vc" ? dionSessionCookies(cookies)
    : cookies.filter(cookie => cookie.name === "remixsid").slice(0, 1);
  return exportBuilder({
    cookies: url.hostname === WTBrowserExport.YANDEX_HOST ? cookies : filtered,
    sourceURL: response.sourceURL,
    selectedTypes: ["cookies"],
  });
}

async function pollingEnabled() {
  const stored = await chrome.storage.local.get({ [POLL_STORAGE_KEY]: true });
  return stored[POLL_STORAGE_KEY] === true;
}

async function syncPollingAlarm() {
  if (await pollingEnabled()) {
    await chrome.alarms.create(POLL_ALARM_NAME, { periodInMinutes: POLL_PERIOD_MINUTES });
  } else {
    await chrome.alarms.clear(POLL_ALARM_NAME);
  }
}

async function setPollingEnabled(enabled) {
  await chrome.storage.local.set({ [POLL_STORAGE_KEY]: enabled === true });
  await syncPollingAlarm();
}

async function pollBrowser() {
  if (!(await pollingEnabled())) {
    return;
  }
  try {
    const command = await WTBrowserBridge.pollRefreshCommand();
    if (!command || command.command !== "update") {
      return;
    }
    const exportData = await createExport({ cookies: true }, { polling: true, command });
    await WTBrowserBridge.sendBrowserExport(exportData);
  } catch {
    // Polling is best-effort. The next 30-second alarm retries without
    // exposing session material or interrupting the user-facing popup.
  }
}

chrome.alarms.onAlarm.addListener((alarm) => {
  if (alarm.name === POLL_ALARM_NAME) {
    return pollBrowser();
  }
});

chrome.runtime.onInstalled.addListener(syncPollingAlarm);
chrome.runtime.onStartup.addListener(syncPollingAlarm);
void syncPollingAlarm();

function exportFilename() {
  return "whitetransport-yandex-telemost-" + new Date().toISOString().replace(/[:.]/g, "-") + ".json";
}

function shellQuote(value) {
  return "'" + String(value).replace(/'/g, "'\\''") + "'";
}

async function importCommand(filePath) {
  const platform = await chrome.runtime.getPlatformInfo();
  // The prepared Apple Silicon helper does not require a repository checkout.
  const asset = platform.os === "mac"
    ? (platform.arch === "arm64" ? "macos-arm64" : "macos-x64")
    : platform.os === "linux" && platform.arch === "x86-64" ? "linux-x64" : "";
  const binary = asset ? '"$HOME/Downloads/wt-browser-credential-bridge-' + asset + '"'
    : ".tmp/cli/wt-browser-credential-bridge";
  return 'WT_NATIVE_GUI_CONFIG_DIR="$HOME/.config/WhiteTransport" ' +
    binary + " --stdin < " + shellQuote(filePath);
}

function waitForDownload(downloadID) {
  return new Promise((resolve, reject) => {
    let settled = false;
    let timeoutID;

    const finish = (callback, value) => {
      if (settled) {
        return;
      }
      settled = true;
      clearTimeout(timeoutID);
      chrome.downloads.onChanged.removeListener(onChanged);
      callback(value);
    };

    const inspect = async () => {
      try {
        const [download] = await chrome.downloads.search({ id: downloadID });
        if (!download) {
          return;
        }
        if (download.state === "complete") {
          if (!download.filename) {
            finish(reject, new Error("Chrome completed the download without a file path"));
            return;
          }
          finish(resolve, download);
          return;
        }
        if (download.state === "interrupted") {
          const reason = download.error ? " (" + download.error + ")" : "";
          finish(reject, new Error("Chrome did not finish saving the JSON" + reason));
        }
      } catch {
        finish(reject, new Error("Could not read the Chrome download state"));
      }
    };

    const onChanged = (delta) => {
      if (delta.id !== downloadID || !delta.state) {
        return;
      }
      if (delta.state.current === "complete" || delta.state.current === "interrupted") {
        void inspect();
      }
    };

    chrome.downloads.onChanged.addListener(onChanged);
    timeoutID = setTimeout(() => {
      finish(reject, new Error("Timed out waiting for the JSON save dialog"));
    }, 5 * 60 * 1000);
    void inspect();
  });
}

async function downloadExport(exportData) {
  const encoded = encodeURIComponent(JSON.stringify(exportData, null, 2));
  const filename = exportFilename();
  const downloadID = await chrome.downloads.download({
    url: "data:application/json;charset=utf-8," + encoded,
    filename,
    saveAs: true,
  });
  const download = await waitForDownload(downloadID);
  return { mode: "download", file_path: download.filename, command: await importCommand(download.filename) };
}

function isExportMessage(message) {
  return message?.type === "wt-send-yandex-telemost" || message?.type === "wt-download-yandex-telemost";
}

function isPollingMessage(message) {
  return ["wt-get-yandex-telemost-polling", "wt-set-yandex-telemost-polling"].includes(message?.type);
}

function isPageReadyMessage(message) {
  return message?.type === "wt-browser-page-ready";
}

function isTrustedTelemostPageSender(sender) {
  if (!sender?.tab || typeof sender.url !== "string") {
    return false;
  }
  try {
    const url = new URL(sender.url);
    return url.protocol === "https:" && url.hostname === WTBrowserExport.YANDEX_HOST;
  } catch {
    return false;
  }
}

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (!isExportMessage(message) && !isPollingMessage(message) && !isPageReadyMessage(message)) {
    return;
  }
  if (isPageReadyMessage(message)) {
    if (isTrustedTelemostPageSender(sender)) {
      void pollBrowser();
      sendResponse({ ok: true });
    }
    return;
  }
  if (!WTBrowserExport.isTrustedPopupSender(sender, chrome.runtime.id)) {
    sendResponse({ ok: false, error: "Export requests are accepted only from the extension popup" });
    return;
  }

  if (message.type === "wt-get-yandex-telemost-polling") {
    pollingEnabled()
      .then(enabled => sendResponse({ ok: true, enabled }))
      .catch((error) => sendResponse({ ok: false, error: error.message }));
    return true;
  }
  if (message.type === "wt-set-yandex-telemost-polling") {
    setPollingEnabled(message.enabled === true)
      .then(() => sendResponse({ ok: true, enabled: message.enabled === true }))
      .catch((error) => sendResponse({ ok: false, error: error.message }));
    return true;
  }

  const selection = message.selection;
  if (!selection || selection.cookies !== true) {
    sendResponse({ ok: false, error: "Invalid export selection" });
    return;
  }

  const download = message.type === "wt-download-yandex-telemost";
  Promise.resolve().then(async () => {
    if (download) return downloadExport(await createExport(selection));
    return WTBrowserBridge.sendBrowserExport(await createExport(selection));
  })
    .then((summary) => sendResponse({ ok: true, summary }))
    .catch((error) => sendResponse({ ok: false, error: error.message }));
  return true;
});
