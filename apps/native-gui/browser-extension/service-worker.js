/* global chrome, importScripts, WTBrowserExport */

"use strict";

importScripts("lib/export-format.js");

async function activeWBStreamTab() {
  const [tab] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
  if (!tab?.id || !tab.url) {
    throw new Error("Open the authenticated WBStream page first");
  }

  const url = new URL(tab.url);
  if (url.protocol !== "https:" || url.hostname !== WTBrowserExport.WB_HOST) {
    throw new Error("Open https://storage-control-stream.wb.ru/ before syncing");
  }
  return tab;
}

async function selectedCookies(includeCookies) {
  if (!includeCookies) {
    return [];
  }
  const cookiesByName = await Promise.all(
    WTBrowserExport.WB_COOKIE_NAMES.map((name) =>
      chrome.cookies.getAll({ url: `https://${WTBrowserExport.WB_HOST}/`, name }),
    ),
  );
  return cookiesByName.flat();
}

async function createExport(selection) {
  const tab = await activeWBStreamTab();
  const response = await chrome.tabs.sendMessage(tab.id, {
    type: "wt-read-wb-auth",
    includeLocalStorage: selection.localStorage,
  });
  if (!response?.ok) {
    throw new Error(response?.error || "Could not read this WBStream tab");
  }

  return WTBrowserExport.buildBrowserExport({
    cookies: await selectedCookies(selection.cookies),
    authSlice: response.authSlice,
    sourceURL: response.sourceURL,
    selectedTypes: Object.entries(selection).filter(([, enabled]) => enabled).map(([type]) => type),
  });
}

function exportFilename() {
  return `whitetransport-wbstream-${new Date().toISOString().replace(/[:.]/g, "-")}.json`;
}

async function downloadExport(exportData) {
  const encoded = encodeURIComponent(JSON.stringify(exportData, null, 2));
  await chrome.downloads.download({
    url: `data:application/json;charset=utf-8,${encoded}`,
    filename: exportFilename(),
    saveAs: true,
  });
}

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message?.type !== "wt-export-wbstream") {
    return;
  }
  if (!WTBrowserExport.isTrustedPopupSender(sender, chrome.runtime.id)) {
    sendResponse({ ok: false, error: "Export requests are accepted only from the extension popup" });
    return;
  }

  const selection = message.selection;
  if (!selection || typeof selection.cookies !== "boolean" || typeof selection.localStorage !== "boolean") {
    sendResponse({ ok: false, error: "Invalid export selection" });
    return;
  }

  createExport(selection)
    .then(downloadExport)
    .then(() => sendResponse({ ok: true }))
    .catch((error) => sendResponse({ ok: false, error: error.message }));
  return true;
});
