/* global chrome, WTBrowserExport */

"use strict";

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message?.type !== "wt-read-wb-auth") {
    return;
  }

  if (!WTBrowserExport.isTrustedExtensionSender(sender, chrome.runtime.id)) {
    sendResponse({ ok: false, error: "Unexpected message sender" });
    return;
  }

  if (location.protocol !== "https:" || location.hostname !== WTBrowserExport.WB_HOST) {
    sendResponse({ ok: false, error: "Unexpected page origin" });
    return;
  }

  // This is deliberately read only after an explicit popup click reaches the worker.
  sendResponse({
    ok: true,
    authSlice: message.includeLocalStorage
      ? localStorage.getItem(WTBrowserExport.WB_STORAGE_KEY)
      : null,
    sourceURL: location.href,
  });
});
