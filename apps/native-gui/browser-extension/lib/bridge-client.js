/* global self */

(() => {
  "use strict";

  const BRIDGE_PATH = "/browser-export";
  const COMMAND_PATH = "/browser-export/command";

  function parsePairing(line) {
    const match = /^READY url=http:\/\/127\.0\.0\.1:([1-9]\d{0,4})\/browser-export nonce=([0-9a-f]{32,128})$/.exec(line.trim());
    if (!match || Number(match[1]) > 65535) {
      throw new Error("Вставьте строку READY из локального CLI WhiteTransport.");
    }
    return { origin: "http://127.0.0.1:" + match[1], nonce: match[2] };
  }

  function endpoint(pairing, path) {
    if (!pairing) throw new Error("Сначала подключите локальный CLI WhiteTransport.");
    const { origin, nonce } = parsePairing("READY url=" + pairing.origin + BRIDGE_PATH + " nonce=" + pairing.nonce);
    return { url: origin + path, headers: { "X-WT-Bridge-Nonce": nonce }, redirect: "error" };
  }

  function safeSummary(summary) {
    if (!summary || !Array.isArray(summary.platforms) || !Number.isInteger(summary.count)) {
      throw new Error("Local bridge returned an invalid summary");
    }
    if (summary.count < 1 || summary.platforms.some((platform) => typeof platform !== "string")) {
      throw new Error("Local bridge returned an invalid credential count");
    }
    return {
      platforms: summary.platforms.slice(),
      count: summary.count,
      config_refreshed: summary.config_refreshed === true,
    };
  }

  async function probePairing(pairing) {
    const { url, headers, redirect } = endpoint(pairing, BRIDGE_PATH);
    let response;
    try {
      response = await fetch(url, { method: "GET", headers, redirect, cache: "no-store" });
    } catch {
      throw new Error("Проверьте, что CLI запущен и Chrome разрешил доступ к локальной сети.");
    }
    if (response.status !== 204) throw new Error("CLI отклонил привязку. Вставьте текущую строку READY.");
  }

  async function sendBrowserExport(exportData, options = {}) {
    if (!exportData || typeof exportData !== "object" || Array.isArray(exportData)) {
      throw new Error("Browser export must be a JSON object");
    }
    const root = typeof self === "object" && self ? self : globalThis;
    const requestFetch = options.fetchImpl || root.fetch;
    if (typeof requestFetch !== "function") {
      throw new Error("Fetch is unavailable for the local bridge");
    }
    const { url, headers, redirect } = endpoint(options.pairing, BRIDGE_PATH);
    let response;
    try {
      response = await requestFetch(url, {
        method: "POST",
        redirect,
        headers: {
          ...headers,
          "Content-Type": "application/json",
        },
        body: JSON.stringify(exportData),
      });
    } catch {
      throw new Error("Local WhiteTransport bridge is unavailable");
    }
    if (!response || !response.ok) {
      throw new Error("Local WhiteTransport bridge rejected the export (HTTP " + (response?.status || 0) + ")");
    }
    let summary;
    try {
      summary = await response.json();
    } catch {
      throw new Error("Local WhiteTransport bridge returned invalid JSON");
    }
    return safeSummary(summary);
  }

  async function pollRefreshCommand(options = {}) {
    const root = typeof self === "object" && self ? self : globalThis;
    const requestFetch = options.fetchImpl || root.fetch;
    if (typeof requestFetch !== "function") {
      throw new Error("Fetch is unavailable for the local bridge");
    }
    const { url, headers, redirect } = endpoint(options.pairing, COMMAND_PATH);
    const response = await requestFetch(url, {
      method: "GET",
      headers,
      redirect,
    });
    if (response.status === 204) {
      return null;
    }
    if (!response.ok) {
      throw new Error("Local WhiteTransport bridge rejected the command (HTTP " + response.status + ")");
    }
    const body = await response.json();
    return body && body.command === "update" ? body : null;
  }

  const root = typeof self === "object" && self ? self : globalThis;
  root.WTBrowserBridge = Object.freeze({
    BRIDGE_PATH,
    COMMAND_PATH,
    parsePairing,
    probePairing,
    sendBrowserExport,
    pollRefreshCommand,
  });
})();
