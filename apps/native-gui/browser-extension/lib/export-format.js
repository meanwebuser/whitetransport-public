/* global self */

(() => {
  "use strict";

  const WB_HOST = "storage-control-stream.wb.ru";
  const WB_COOKIE_NAMES = Object.freeze([
    "x_wbaas_token",
    "wbx-validation-key",
    "_wbauid",
  ]);
  const WB_STORAGE_KEY = "wb_auth_auth_slice";
  const EXPORT_TYPES = Object.freeze(["cookies", "localStorage"]);

  function selectedExportTypes(selectedTypes) {
    const values = selectedTypes === undefined ? EXPORT_TYPES : selectedTypes;
    if (!Array.isArray(values) || values.length === 0) {
      throw new Error("Select at least one data type to export");
    }
    if (new Set(values).size !== values.length || values.some((value) => !EXPORT_TYPES.includes(value))) {
      throw new Error("Unsupported browser-export data type");
    }
    return values;
  }

  function isOfficialCookieDomain(domain) {
    const normalized = String(domain || "").replace(/^\./, "").toLowerCase();
    return normalized === WB_HOST || WB_HOST.endsWith(`.${normalized}`);
  }

  /** Accept messages only from this installed extension, never from a web origin. */
  function isTrustedExtensionSender(sender, extensionID) {
    if (!extensionID || sender?.id !== extensionID) {
      return false;
    }
    if (!sender.url) {
      return true;
    }
    try {
      const url = new URL(sender.url);
      return url.protocol === "chrome-extension:" && url.hostname === extensionID;
    } catch {
      return false;
    }
  }

  /** The export action is privileged and may only originate from popup.html. */
  function isTrustedPopupSender(sender, extensionID) {
    if (!isTrustedExtensionSender(sender, extensionID) || sender.tab || !sender.url) {
      return false;
    }
    const url = new URL(sender.url);
    return url.pathname === "/popup.html";
  }

  /** Build the browser-export JSON accepted by the native client. */
  function buildBrowserExport({ cookies = [], authSlice, sourceURL, selectedTypes }) {
    const source = new URL(sourceURL);
    if (source.protocol !== "https:" || source.hostname !== WB_HOST) {
      throw new Error("WBStream export must originate from the official HTTPS host");
    }
    const types = selectedExportTypes(selectedTypes);
    const includeCookies = types.includes("cookies");
    const includeLocalStorage = types.includes("localStorage");

    const exportedCookies = includeCookies ? cookies
      .filter((cookie) => WB_COOKIE_NAMES.includes(cookie.name) && isOfficialCookieDomain(cookie.domain))
      .map((cookie) => ({
        name: cookie.name,
        value: cookie.value,
        domain: cookie.domain,
        path: cookie.path,
        secure: Boolean(cookie.secure),
        httpOnly: Boolean(cookie.httpOnly),
        sameSite: cookie.sameSite || "",
        expirationDate: cookie.expirationDate || 0,
      })) : [];
    const exportedLocalStorage = includeLocalStorage && authSlice
      ? [{ key: WB_STORAGE_KEY, value: authSlice }]
      : [];

    if (exportedCookies.length === 0 && exportedLocalStorage.length === 0) {
      throw new Error("No WBStream session data is available in this browser profile");
    }

    return {
      version: 1,
      exportedAt: new Date().toISOString(),
      // Paths, room identifiers, query parameters, and fragments are not credentials.
      source: { url: `https://${WB_HOST}/`, host: WB_HOST },
      selectedTypes: [...types],
      cookies: exportedCookies,
      localStorage: exportedLocalStorage,
      sessionStorage: [],
    };
  }

  self.WTBrowserExport = Object.freeze({
    WB_HOST,
    WB_COOKIE_NAMES,
    WB_STORAGE_KEY,
    buildBrowserExport,
    isTrustedExtensionSender,
    isTrustedPopupSender,
  });
})();
