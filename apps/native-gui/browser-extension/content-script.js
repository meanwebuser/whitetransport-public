/* global chrome, WTBrowserExport */

"use strict";

chrome.runtime.onMessage.addListener((message, sender, sendResponse) => {
  if (message?.type !== "wt-read-yandex-telemost-auth" && message?.type !== "wt-read-browser-auth") {
    return;
  }

  if (!WTBrowserExport.isTrustedExtensionSender(sender, chrome.runtime.id)) {
    sendResponse({ ok: false, error: "Unexpected message sender" });
    return;
  }

  const allowed = {
    "telemost.yandex.ru": WTBrowserExport.YANDEX_COOKIE_NAMES,
    "dion.vc": ["vc-access-token", "vc-refresh-token"],
    "vk.com": ["remixsid"],
    "vk.ru": ["remixsid"],
  }[location.hostname];
  if (location.protocol !== "https:" || !Array.isArray(allowed)) {
    sendResponse({ ok: false, error: "Unexpected page origin" });
    return;
  }

  // This is deliberately read only after an explicit popup click reaches the worker.
  // Chrome's cookies API can omit the page's first-party Yandex session jar,
  // while document.cookie still exposes the non-HttpOnly session material.
  sendResponse({
    ok: true,
    sourceURL: location.origin + location.pathname,
    cookies: message.includeCookies === false
      ? []
      : WTBrowserExport.parseCookieHeader(document.cookie, location.hostname).filter(cookie => allowed.includes(cookie.name)),
  });
});

if (location.hostname === "telemost.yandex.ru") {
  void chrome.runtime.sendMessage({ type: "wt-browser-page-ready" });
}
