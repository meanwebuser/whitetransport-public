/* global chrome, WTBrowserBridge */

"use strict";

const sendButton = document.querySelector("#export");
const saveButton = document.querySelector("#save-json");
const status = document.querySelector("#status");
const importCommandField = document.querySelector("#import-command");
const includeCookies = document.querySelector("#include-cookies");
const autoRefresh = document.querySelector("#auto-refresh");
const bridgeState = document.querySelector("#bridge-state");
const pairForm = document.querySelector("#pair-form");
const pairLine = document.querySelector("#pair-line");
const unpairButton = document.querySelector("#unpair");

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
  const paired = response?.paired === true;
  autoRefresh.disabled = !paired;
  unpairButton.hidden = !paired;
  sendButton.disabled = !paired;
  setReadyState(paired ? "ready" : "unknown", paired ? "CLI привязан" : "CLI не привязан");
}

async function pairBridge(line) {
  try {
    if (line) await WTBrowserBridge.probePairing(WTBrowserBridge.parsePairing(line));
    const response = await chrome.runtime.sendMessage({ type: "wt-pair-bridge", line });
    if (!response?.ok) throw new Error(response?.error || "Не удалось сохранить привязку CLI.");
    pairLine.value = "";
    await loadPollingState();
    setStatus(response.paired ? "Привязка сохранена. Можно включить polling." : "CLI отключён.", "success");
  } catch (error) {
    setStatus(error.message, "error");
  }
}

async function savePollingState() {
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
pairForm.addEventListener("submit", event => {
  event.preventDefault();
  void pairBridge(pairLine.value);
});
unpairButton.addEventListener("click", () => void pairBridge(""));
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
