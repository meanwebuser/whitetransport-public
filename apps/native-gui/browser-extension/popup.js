/* global chrome, WTBrowserBridge */

"use strict";

const sendButton = document.querySelector("#export");
const saveButton = document.querySelector("#save-json");
const status = document.querySelector("#status");
const importCommandField = document.querySelector("#import-command");
const includeCookies = document.querySelector("#include-cookies");
const autoRefresh = document.querySelector("#auto-refresh");
const bridgeState = document.querySelector("#bridge-state");

function setStatus(message, kind = "") {
  status.textContent = message;
  status.className = "status" + (kind ? " " + kind : "");
}

function setReadyState(kind, text) {
  bridgeState.className = "state state-" + kind;
  bridgeState.textContent = text;
}

async function loadPollingState() {
  const response = await chrome.runtime.sendMessage({ type: "wt-get-yandex-telemost-polling" });
  autoRefresh.checked = response?.enabled === true;
  setReadyState("unknown", autoRefresh.checked ? "Опрос CLI включён" : "Опрос CLI выключен");
}

async function savePollingState() {
  if (autoRefresh.checked) {
    try {
      await WTBrowserBridge.probeBridge();
    } catch (error) {
      autoRefresh.checked = false;
      setStatus(error.message, "error");
      return;
    }
  }
  const response = await chrome.runtime.sendMessage({
    type: "wt-set-yandex-telemost-polling",
    enabled: autoRefresh.checked,
  });
  if (!response?.ok) {
    autoRefresh.checked = !autoRefresh.checked;
    setStatus(response?.error || "Не удалось изменить автообновление", "error");
    return;
  }
  setStatus(autoRefresh.checked
    ? "Быстрое автообновление включено: каждые 30 секунд."
    : "Автообновление выключено.", "success");
  await loadPollingState();
}

function selection() {
  return { cookies: includeCookies.checked };
}

async function runExport(type) {
  const selected = selection();
  if (!selected.cookies) {
    setStatus("Включите экспорт cookies.", "error");
    return;
  }

  sendButton.disabled = true;
  saveButton.disabled = true;
  sendButton.setAttribute("aria-busy", "true");
  saveButton.setAttribute("aria-busy", "true");
  importCommandField.hidden = true;
  setStatus(type === "wt-send-yandex-telemost"
    ? "Проверяем вкладку и локальный bridge…"
    : "Готовим JSON-файл…");

  try {
    if (type === "wt-send-yandex-telemost") await WTBrowserBridge.probeBridge();
    const response = await chrome.runtime.sendMessage({ type, selection: selected });
    if (!response?.ok) {
      throw new Error(response?.error || "Не удалось подготовить экспорт");
    }
    if (type === "wt-download-yandex-telemost") {
      importCommandField.value = response.summary?.command || "";
      importCommandField.hidden = !importCommandField.value;
      setStatus("JSON сохранён. Команда импорта показана ниже.", "success");
      return;
    }
    const summary = response.summary || {};
    const count = Number.isInteger(summary.count) ? summary.count : 0;
    setStatus("Сессия передана в WhiteTransport (записей: " + count + ").", "success");
    setReadyState("ready", "CLI принял сессию");
  } catch (error) {
    setStatus(type === "wt-send-yandex-telemost"
      ? error.message + " Сохраните JSON отдельной кнопкой для ручного импорта."
      : error.message, "error");
  } finally {
    sendButton.disabled = false;
    saveButton.disabled = false;
    sendButton.removeAttribute("aria-busy");
    saveButton.removeAttribute("aria-busy");
  }
}

sendButton.addEventListener("click", () => runExport("wt-send-yandex-telemost"));
saveButton.addEventListener("click", () => runExport("wt-download-yandex-telemost"));
autoRefresh.addEventListener("change", () => void savePollingState());
document.querySelector("#copy-meeting").addEventListener("click", async () => {
  try {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    const url = new URL(tab?.url);
    if (url.origin !== "https://telemost.yandex.ru" || url.username || url.password ||
        !/^\/j\/[A-Za-z0-9_-]+$/.test(url.pathname)) {
      throw new Error();
    }
    await navigator.clipboard.writeText(url.origin + url.pathname);
    setStatus("Ссылка скопирована.", "success");
  } catch {
    setStatus("Откройте встречу Телемоста.", "error");
  }
});
void loadPollingState().catch(() => {
  autoRefresh.checked = false;
  setReadyState("error", "Bridge недоступен");
  setStatus("Не удалось прочитать состояние bridge. Повторите действие позже.", "error");
});
