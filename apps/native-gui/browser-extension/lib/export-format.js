/* global self */

(() => {
  "use strict";

  const YANDEX_HOST = "telemost.yandex.ru";
  const YANDEX_COOKIE_NAMES = Object.freeze([
    "Session_id",
    "sessionid2",
    "sessar",
    "L",
    "yandex_login",
    "ymex",
    "yandexuid",
    "yuidss",
    "yp",
    "cycada",
    "_yasc",
    "_mssngr_flags",
    "_mssngr_saved_state",
    "i",
  ]);
  const YANDEX_AUTH_COOKIE_NAMES = Object.freeze([
    "Session_id",
    "sessionid2",
    "sessar",
    "L",
    "yandex_login",
    "ymex",
    "yp",
    "cycada",
    "_yasc",
  ]);
  const EXPORT_TYPES = Object.freeze(["cookies"]);

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
    return normalized === "yandex.ru" || normalized.endsWith(".yandex.ru");
  }

  function parseCookieHeader(header, domain = YANDEX_HOST) {
    return String(header || "")
      .split(";")
      .map((part) => part.trim())
      .map((part) => {
        const separator = part.indexOf("=");
        if (separator <= 0) {
          return null;
        }
        return {
          name: part.slice(0, separator).trim(),
          value: part.slice(separator + 1),
          domain,
          path: "/",
          secure: true,
          httpOnly: false,
          sameSite: "",
          expirationDate: 0,
        };
      })
      .filter((cookie) => cookie && cookie.name && cookie.value);
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
  function buildBrowserExport({ cookies = [], sourceURL, selectedTypes }) {
    const source = new URL(sourceURL);
    if (source.protocol !== "https:" || source.hostname !== YANDEX_HOST) {
      throw new Error("Yandex Telemost export must originate from the official HTTPS host");
    }
    const types = selectedExportTypes(selectedTypes);
    const includeCookies = types.includes("cookies");

    const exportedCookies = includeCookies ? cookies
      .filter((cookie) => YANDEX_COOKIE_NAMES.includes(cookie.name) && isOfficialCookieDomain(cookie.domain))
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
    const exportedLocalStorage = [];

    const hasSession = exportedCookies.some((cookie) =>
      YANDEX_AUTH_COOKIE_NAMES.includes(cookie.name) &&
      typeof cookie.value === "string" && cookie.value.trim().length > 0);
    if (!hasSession) {
      throw new Error("No Yandex Telemost session data is available in this browser profile");
    }

    return {
      version: 1,
      exportedAt: new Date().toISOString(),
      // Keep only the room path needed by Telemost; drop query and fragment data.
      source: { url: source.origin + (source.pathname || "/"), host: YANDEX_HOST },
      selectedTypes: [...types],
      cookies: exportedCookies,
      localStorage: exportedLocalStorage,
      sessionStorage: [],
    };
  }

  function buildGenericBrowserExport({ cookies = [], sourceURL, selectedTypes }) {
    const source = new URL(sourceURL);
    if (source.protocol !== "https:" || !source.hostname) {
      throw new Error("Browser export must originate from an HTTPS page");
    }
    const types = selectedExportTypes(selectedTypes);
    const exportedCookies = types.includes("cookies") ? cookies
      .filter((cookie) => cookie && typeof cookie.name === "string" && cookie.name && typeof cookie.value === "string" && cookie.value)
      .map((cookie) => ({
        name: cookie.name,
        value: cookie.value,
        domain: cookie.domain || source.hostname,
        path: cookie.path || "/",
        secure: Boolean(cookie.secure),
        httpOnly: Boolean(cookie.httpOnly),
        sameSite: cookie.sameSite || "",
        expirationDate: cookie.expirationDate || 0,
      })) : [];
    if (exportedCookies.length === 0) {
      throw new Error("No browser cookies are available in this page");
    }
    return {
      version: 1,
      exportedAt: new Date().toISOString(),
      source: { url: source.origin + (source.pathname || "/"), host: source.hostname },
      selectedTypes: [...types],
      cookies: exportedCookies,
      localStorage: [],
      sessionStorage: [],
    };
  }

  self.WTBrowserExport = Object.freeze({
    YANDEX_HOST,
    YANDEX_COOKIE_NAMES,
    YANDEX_AUTH_COOKIE_NAMES,
    EXPORT_TYPES,
    buildBrowserExport,
    buildGenericBrowserExport,
    parseCookieHeader,
    isTrustedExtensionSender,
    isTrustedPopupSender,
  });
})();
