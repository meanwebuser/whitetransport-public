/* global chrome */

"use strict";

const OFFICIAL_ORIGIN = "https://telemost.yandex.ru";
const JOIN_RE = /^\/j\/([A-Za-z0-9_-]+)$/;

function meetingURL(rawURL) {
  try {
    const url = new URL(rawURL);
    if (url.origin !== OFFICIAL_ORIGIN || url.username || url.password) {
      return null;
    }
    const match = url.pathname.match(JOIN_RE);
    if (!match) {
      return null;
    }
    return OFFICIAL_ORIGIN + "/j/" + match[1];
  } catch {
    return null;
  }
}

function show(text, ok) {
  const node = document.querySelector("#status");
  node.textContent = text;
  node.className = ok ? "ok" : "err";
}

document.querySelector("#copy").addEventListener("click", async () => {
  try {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    const clean = meetingURL(tab?.url);
    if (!clean) {
      show("Откройте ссылку на встречу Телемоста", false);
      return;
    }
    await navigator.clipboard.writeText(clean);
    show("Ссылка скопирована", true);
  } catch {
    show("Не удалось скопировать ссылку", false);
  }
});
