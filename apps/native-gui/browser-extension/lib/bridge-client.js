/* global self */

(() => {
  "use strict";

  const BRIDGE_PATH = "/browser-export";
  const COMMAND_PATH = "/browser-export/command";

  function validateConnection(connection) {
    const match = /^http:\/\/127\.0\.0\.1:([1-9]\d{0,4})$/.exec(connection?.origin);
    if (!match || Number(match[1]) > 65535 || !/^[0-9a-f]{64}$/.test(connection?.nonce)) {
      throw new Error("Запустите CLI из папки с расширением WhiteTransport.");
    }
    return connection;
  }

  async function loadConnection() {
    let connection;
    try {
      const url = chrome.runtime.getURL("browser-bridge.local.json");
      const response = await fetch(url, { cache: "no-store" });
      if (response.ok) connection = await response.json();
    } catch {
      // The CLI writes this file after it starts listening.
    }
    return validateConnection(connection);
  }

  function endpoint(pairing, path) {
    const { origin, nonce } = validateConnection(pairing);
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

  async function probeBridge() {
    const { url, headers, redirect } = endpoint(await loadConnection(), BRIDGE_PATH);
    let response;
    try {
      response = await fetch(url, { method: "GET", headers, redirect, cache: "no-store" });
    } catch {
      throw new Error("Проверьте, что CLI запущен и Chrome разрешил доступ к локальной сети.");
    }
    if (response.status !== 204) throw new Error("Запущенный CLI не совпадает с локальной конфигурацией расширения.");
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
    const { url, headers, redirect } = endpoint(options.connection || await loadConnection(), BRIDGE_PATH);
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
    const { url, headers, redirect } = endpoint(options.connection || await loadConnection(), COMMAND_PATH);
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
    probeBridge,
    validateConnection,
    sendBrowserExport,
    pollRefreshCommand,
  });
})();
