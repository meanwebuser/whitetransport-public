/* global chrome */

"use strict";

const button = document.querySelector("#export");
const status = document.querySelector("#status");
const includeCookies = document.querySelector("#include-cookies");
const includeLocalStorage = document.querySelector("#include-local-storage");

button.addEventListener("click", async () => {
  const selection = {
    cookies: includeCookies.checked,
    localStorage: includeLocalStorage.checked,
  };
  if (!selection.cookies && !selection.localStorage) {
    status.textContent = "Выберите cookies и/или localStorage для экспорта.";
    return;
  }

  button.disabled = true;
  status.textContent = "Проверяем текущую вкладку…";

  try {
    const response = await chrome.runtime.sendMessage({ type: "wt-export-wbstream", selection });
    if (!response?.ok) {
      throw new Error(response?.error || "Не удалось подготовить экспорт");
    }
    status.textContent = "Файл сохранён. Импортируйте его в WhiteTransport.";
  } catch (error) {
    status.textContent = error.message;
  } finally {
    button.disabled = false;
  }
});
