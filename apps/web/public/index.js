import LibcurlClient from "/libcurl/index.mjs";
import { BareClient, BareMuxConnection } from "/baremux/index.mjs";

"use strict";

/* ── DOM refs ── */
const form = document.getElementById("sj-form");
const address = document.getElementById("sj-address");
const searchEngine = document.getElementById("sj-search-engine");
const error = document.getElementById("sj-error");
const errorCode = document.getElementById("sj-error-code");
const stage = document.getElementById("browser-stage");
const favoritesSection = document.getElementById("favorites-section");
const favoritesList = document.getElementById("favorites-list");
const heroCard = document.getElementById("hero-card");
const browserBar = document.getElementById("browser-bar");
const barPeek = document.getElementById("bar-peek");
const barStatus = document.getElementById("bar-status");
const navMenu = document.getElementById("nav-menu");
const navFav = document.getElementById("nav-fav");
const loadingOverlay = document.getElementById("loading-overlay");
const loader = window.MobileBrowserLoaders?.create(loadingOverlay);
const settingsMenu = document.getElementById("settings-menu");
const settingLogs = document.getElementById("setting-logs");
const settingLogLevelRow = document.getElementById("setting-log-level-row");
const settingLogLevel = document.getElementById("setting-log-level");
const settingBarPosition = document.getElementById("setting-bar-position");
const settingAutohide = document.getElementById("setting-autohide");
const settingLoadingAnimation = document.getElementById("setting-loading-animation");
const settingTransport = document.getElementById("setting-transport");
const settingBareTransport = document.getElementById("setting-bare-transport");
const settingHome = document.getElementById("setting-home");
const debugConsole = document.getElementById("debug-console");
const debugLog = document.getElementById("debug-log");
const debugCopy = document.getElementById("debug-copy");
const debugClear = document.getElementById("debug-clear");

/* ── Profile & modal refs ── */
const profileBtn = document.getElementById("setting-profiles");
const profileModal = document.getElementById("profile-modal");
const profileModalClose = document.getElementById("profile-modal-close");
const profileListEl = document.getElementById("profile-list");
const profileNewBtn = document.getElementById("profile-new");
const profileActiveName = document.getElementById("profile-active-name");
const profilePanel = document.getElementById("profile-panel");

const bookmarkModal = document.getElementById("bookmark-modal");
const bookmarkModalClose = document.getElementById("bookmark-modal-close");
const bookmarkNameInput = document.getElementById("bookmark-name-input");
const bookmarkSaveBtn = document.getElementById("bookmark-save-btn");
const bookmarkUrlPreview = document.getElementById("bookmark-url-preview");

const importModal = document.getElementById("import-modal");
const importAccept = document.getElementById("import-accept");
const importDecline = document.getElementById("import-decline");
const importInfo = document.getElementById("import-info");

/* ── Constants ── */
const FAVORITES_KEY = "mobile-browser:favorites";
const HISTORY_KEY = "mobile-browser:history";
const SETTINGS_KEY = "mobile-browser:settings";
const DEBUG_LOG_KEY = "mobile-browser:debug-log";
const PROFILES_KEY = "mobile-browser:profiles";
const ACTIVE_PROFILE_KEY = "mobile-browser:active-profile";
const COLLAPSE_DELAY = 2100;
const MAX_DEBUG_LINES = 600;

const WHITETRANSPORT_ROOM_DISCOVERY_URL = "/web/_wt/current-room?source=ok&count=50";
const LEGACY_WHITETRANSPORT_ENABLED_KEY = "mobile-browser:whitetransport-enabled";
const TRANSPORT_WB = "wb";
const TRANSPORT_WISP = "wisp";
const TRANSPORT_SCRAMJET_V1 = "scramjet-v1";
const TRANSPORT_ULTRAVIOLET = "ultraviolet";
const TRANSPORT_RENDER = "render";
const TRANSPORT_ANTICORS = "anticors";
const ANTICORS_PROXY_PREFIX = "/web/";
const SCRAMJET_V1_PREFIX = "/scramjet-v1/service/";
const BARE_TRANSPORT_LIBCURL = "libcurl";
const BARE_TRANSPORT_EPOXY = "epoxy";

function normalizeTransport(value) {
        if (value === TRANSPORT_WISP) return TRANSPORT_WISP;
        if (value === TRANSPORT_SCRAMJET_V1) return TRANSPORT_SCRAMJET_V1;
        if (value === TRANSPORT_ULTRAVIOLET) return TRANSPORT_ULTRAVIOLET;
        if (value === TRANSPORT_RENDER) return TRANSPORT_RENDER;
        if (value === TRANSPORT_ANTICORS) return TRANSPORT_ANTICORS;
        return TRANSPORT_WB;
}

function selectedTransport() {
        const configured = normalizeTransport(settings?.transport);
        if (settings?.transport) return configured;
        const legacy = localStorage.getItem(LEGACY_WHITETRANSPORT_ENABLED_KEY);
        if (legacy === "0" || legacy === "false") return TRANSPORT_WISP;
        return TRANSPORT_WB;
}

function normalizeBareTransport(value) {
        if (value === BARE_TRANSPORT_EPOXY) return BARE_TRANSPORT_EPOXY;
        return BARE_TRANSPORT_LIBCURL;
}

function selectedBareTransport() {
        return normalizeBareTransport(settings?.bareTransport);
}

function bareTransportLabel(value = selectedBareTransport()) {
        if (value === BARE_TRANSPORT_EPOXY) return "Epoxy";
        return "libcurl";
}

function bareTransportModulePath(value = selectedBareTransport()) {
        if (value === BARE_TRANSPORT_EPOXY) return "/epoxy/index.mjs";
        return "/libcurl/index.mjs";
}

function isWhiteTransportEnabled() {
        return selectedTransport() === TRANSPORT_WB;
}

function isRemoteRenderEnabled() {
        return selectedTransport() === TRANSPORT_RENDER;
}

function isAntiCorsProxyEnabled() {
        return selectedTransport() === TRANSPORT_ANTICORS;
}

function isScramjetV1Enabled() {
        return selectedTransport() === TRANSPORT_SCRAMJET_V1;
}

function isUltravioletEnabled() {
        return selectedTransport() === TRANSPORT_ULTRAVIOLET;
}

function transportLabel(value = selectedTransport()) {
        if (value === TRANSPORT_WISP) return "Scramjet v2 / Wisp";
        if (value === TRANSPORT_SCRAMJET_V1) return "Scramjet v1";
        if (value === TRANSPORT_ULTRAVIOLET) return "Ultraviolet";
        if (value === TRANSPORT_RENDER) return "Серверный браузер";
        if (value === TRANSPORT_ANTICORS) return "Anti-CORS /web";
        return "WB/LiveKit";
}

function antiCorsProxyUrl(url) {
        return ANTICORS_PROXY_PREFIX + url;
}

function enableCredentiallessIframe(iframe) {
        try {
                iframe.credentialless = true;
                iframe.setAttribute("credentialless", "");
        } catch {}
}


function ultravioletProxyUrl(url) {
        return window.__uv$config.prefix + window.__uv$config.encodeUrl(url);
}


async function ensureWhiteTransportBareMux() {
        if (whiteTransportBareMuxReady) return whiteTransportBareMuxReady;
        whiteTransportBareMuxReady = (async () => {
                const wispUrl = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/wisp/?bootstrap=anticors/`;
                const bareTransport = selectedBareTransport();
                const connection = new BareMuxConnection("/baremux/worker.js");
                await connection.setTransport(bareTransportModulePath(bareTransport), [{ wisp: wispUrl }]);
                whiteTransportBareClient = new BareClient("/baremux/worker.js");
                console.info("WhiteTransport anti-CORS bootstrap transport ready", { wispUrl, bareTransport: bareTransportLabel(bareTransport) });
                return whiteTransportBareClient;
        })().catch((err) => {
                whiteTransportBareMuxReady = null;
                whiteTransportBareClient = null;
                throw err;
        });
        return whiteTransportBareMuxReady;
}

async function whiteTransportProxyFetch(input, init = {}) {
        const url = typeof input === "string" ? input : input?.url;
        const headers = Object.fromEntries(new Headers(init.headers || input?.headers || {}).entries());
        let body = init.body || "";
        if (body && typeof body !== "string") body = await new Response(body).text();
        return fetch("/api/whitetransport/wb-proxy", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify({
                        url: String(url),
                        method: init.method || input?.method || "GET",
                        headers,
                        body,
                }),
        });
}

async function whiteTransportFetch(input, init = {}) {
        const url = typeof input === "string" ? input : input?.url;
        if (!url || !String(url).startsWith("https://stream.wb.ru/")) return fetch(input, init);
        try {
                const client = await ensureWhiteTransportBareMux();
                const response = await client.fetch(input, init);
                console.debug("WhiteTransport BareMux fetch", String(url), response.status);
                return response;
        } catch (err) {
                console.warn("WhiteTransport BareMux fetch failed; falling back to server proxy", String(url), err);
                return whiteTransportProxyFetch(input, init);
        }
}

function installWhiteTransportIfAvailable() {
        if (!isWhiteTransportEnabled()) {
                console.info("whitetransport disabled by settings", { transport: selectedTransport() });
                return false;
        }
        if (!window.WhiteTransport?.installWispOverWb) {
                console.warn("whitetransport bundle is not loaded; falling back to normal Wisp");
                return false;
        }
        if (window.__whiteTransportInstalled) return true;
        window.__whiteTransportInstalled = window.WhiteTransport.installWispOverWb({
                roomDiscoveryUrl: WHITETRANSPORT_ROOM_DISCOVERY_URL,
                displayName: "WhiteTransport Browser",
                match: /\/wisp\/?$/i,
                fetchImpl: (input, init = {}) => {
                        const url = typeof input === "string" ? input : input?.url;
                        if (url && String(url).startsWith("https://stream.wb.ru/")) {
                                return whiteTransportProxyFetch(input, init);
                        }
                        return fetch(input, init);
                },
                onStatus: (status) => console.info("[whitetransport]", status),
        });
        console.info("whitetransport installed", { roomDiscoveryUrl: WHITETRANSPORT_ROOM_DISCOVERY_URL });
        return true;
}


/* ── State ── */
const LOG_LEVELS = { debug: 10, log: 20, info: 30, warn: 40, error: 50 };
const defaultSettings = { logs: false, logLevel: "debug", barPosition: "bottom", autohide: true, loadingAnimation: true, transport: TRANSPORT_ULTRAVIOLET, bareTransport: BARE_TRANSPORT_LIBCURL };
const debugEntries = readJson(DEBUG_LOG_KEY, []);
const nativeConsole = { log: console.log.bind(console), info: console.info.bind(console), warn: console.warn.bind(console), error: console.error.bind(console), debug: console.debug.bind(console) };

const { Controller } = window.$scramjetController;
const { defaultConfigDev } = window.$scramjet;

let controller = null;
let controllerReady = null;
let scramjetV1Ready = null;
let scramjetV1Controller = null;
let ultravioletReady = null;
let ultravioletBareMuxReady = null;
let whiteTransportBareMuxReady = null;
let whiteTransportBareClient = null;
let activeFrame = null;
let currentUrl = "";
let historyStack = readJson(HISTORY_KEY, []);
let favorites = readJson(FAVORITES_KEY, []);
let settings = { ...defaultSettings, ...readJson(SETTINGS_KEY, {}) };
installConsoleCapture();
let statusTimer = null;
let loadingTimer = null;
let collapseTimer = null;
let suppressPop = false;
let routeErrorRetryUrl = "";
let routeErrorRetryTimer = null;
let remoteBrowser = null;

/* ── Profiles ── */
let profiles = readJson(PROFILES_KEY, []);
let activeProfileId = localStorage.getItem(ACTIVE_PROFILE_KEY) || null;
let pendingBookmarkUrl = null;
let pendingImportProfile = null;

function generateId() {
        return Date.now().toString(36) + Math.random().toString(36).slice(2, 8);
}

function getActiveProfile() {
        if (!activeProfileId) return null;
        return profiles.find(p => p.id === activeProfileId) || null;
}

function initDefaultProfile() {
        if (profiles.length === 0) {
                const defaultProfile = {
                        id: generateId(),
                        name: "По умолчанию",
                        favorites: readJson(FAVORITES_KEY, []),
                        settings: { ...defaultSettings, ...readJson(SETTINGS_KEY, {}) },
                        siteData: {},
                        createdAt: Date.now(),
                };
                profiles.push(defaultProfile);
                activeProfileId = defaultProfile.id;
                writeJson(PROFILES_KEY, profiles);
                localStorage.setItem(ACTIVE_PROFILE_KEY, activeProfileId);
        }
}

function saveProfile() {
        const profile = getActiveProfile();
        if (!profile) return;
        profile.favorites = [...favorites];
        profile.settings = { ...settings };
        writeJson(PROFILES_KEY, profiles);
}

function switchProfile(profileId) {
        /* Save current profile state before switching */
        saveProfile();

        activeProfileId = profileId;
        localStorage.setItem(ACTIVE_PROFILE_KEY, activeProfileId);

        const profile = getActiveProfile();
        if (!profile) return;

        /* Load profile data */
        favorites = profile.favorites ? [...profile.favorites] : [];
        settings = { ...defaultSettings, ...profile.settings };
        writeJson(FAVORITES_KEY, favorites);
        saveSettings();
        renderFavorites();
        applySettings();
        updateControls();
        renderProfiles();
}

function createProfile(name) {
        const profile = {
                id: generateId(),
                name: name || "Новый профиль",
                favorites: [],
                settings: { ...defaultSettings },
                siteData: {},
                createdAt: Date.now(),
        };
        profiles.push(profile);
        writeJson(PROFILES_KEY, profiles);
        switchProfile(profile.id);
        return profile;
}

function deleteProfile(profileId) {
        if (profiles.length <= 1) {
                setStatus("Нельзя удалить последний профиль");
                return;
        }
        profiles = profiles.filter(p => p.id !== profileId);
        writeJson(PROFILES_KEY, profiles);
        if (activeProfileId === profileId) {
                switchProfile(profiles[0].id);
        } else {
                renderProfiles();
        }
}

function renameProfile(profileId, newName) {
        const profile = profiles.find(p => p.id === profileId);
        if (profile) {
                profile.name = newName;
                writeJson(PROFILES_KEY, profiles);
                renderProfiles();
        }
}

/* ── Cookie / localStorage capture ── */
function captureSiteData() {
        const profile = getActiveProfile();
        if (!profile || !currentUrl) {
                setStatus("Нет активного профиля или URL");
                return;
        }

        let capturedCookies = "";
        let capturedStorage = {};

        try {
                const iframeEl = activeFrame?.element;
                if (!iframeEl) {
                        setStatus("Нет активного iframe");
                        return;
                }

                const iframeDoc = iframeEl.contentDocument;
                const iframeWin = iframeEl.contentWindow;

                /* Capture cookies from iframe */
                if (iframeDoc) {
                        capturedCookies = iframeDoc.cookie || "";
                        const cookieCount = parseCookieString(capturedCookies).length;
                        console.info("Captured cookies for", currentUrl, "count:", cookieCount);
                }

                /* Capture localStorage — skip our own app keys */
                if (iframeWin && iframeWin.localStorage) {
                        capturedStorage = {};
                        for (let i = 0; i < iframeWin.localStorage.length; i++) {
                                const key = iframeWin.localStorage.key(i);
                                if (!key) continue;
                                /* Skip our own app keys */
                                if (key.startsWith("mobile-browser:")) continue;
                                try {
                                        const value = iframeWin.localStorage.getItem(key);
                                        /* Skip null/undefined values */
                                        if (value === null || value === undefined) continue;
                                        capturedStorage[key] = value;
                                } catch (e) {
                                        console.warn("Cannot read localStorage key:", key, e);
                                }
                        }
                        console.info("Captured localStorage for", currentUrl, "count:", Object.keys(capturedStorage).length);
                }
        } catch (err) {
                console.warn("Cannot access iframe content directly, trying postMessage fallback", err);
                /* Fallback: try postMessage to ask the iframe to send data back */
                try {
                        const iframeWin = activeFrame?.element?.contentWindow;
                        if (iframeWin) {
                                const msgChannel = new MessageChannel();
                                msgChannel.port1.onmessage = (event) => {
                                        const data = event.data;
                                        if (data && data.type === "captured-site-data") {
                                                capturedCookies = data.cookies || "";
                                                capturedStorage = data.storage || {};
                                                saveCapturedData();
                                        }
                                };
                                iframeWin.postMessage({ type: "capture-site-data" }, "*", [msgChannel.port2]);
                                setStatus("Ожидание данных от сайта…");
                                return;
                        }
                } catch (e2) {
                        console.error("postMessage fallback also failed", e2);
                }
                setStatus("Не удалось получить данные сайта");
                return;
        }

        saveCapturedData();

        function saveCapturedData() {
                const origin = new URL(currentUrl).origin;
                if (!profile.siteData) profile.siteData = {};
                profile.siteData[origin] = {
                        cookies: capturedCookies,
                        storage: capturedStorage,
                        capturedAt: Date.now(),
                        url: currentUrl,
                };
                writeJson(PROFILES_KEY, profiles);
                const cookieCount = parseCookieString(capturedCookies).length;
                const storageCount = Object.keys(capturedStorage).length;
                setStatus(`Сохранено: ${cookieCount} кук, ${storageCount} localStorage`);
        }
}

/* ── Cookie helpers ── */
function parseCookieString(str) {
        if (!str) return [];
        const result = [];
        let current = "";
        let inEscape = false;
        for (let i = 0; i < str.length; i++) {
                const ch = str[i];
                if (inEscape) {
                        current += ch;
                        inEscape = false;
                } else if (ch === "\\") {
                        current += ch;
                        inEscape = true;
                } else if (ch === ";" && !current.match(/\{$/)) {
                        const trimmed = current.trim();
                        if (trimmed) result.push(trimmed);
                        current = "";
                } else {
                        current += ch;
                }
        }
        const trimmed = current.trim();
        if (trimmed) result.push(trimmed);
        return result;
}

function parseSingleCookie(cookieStr) {
        const eqIdx = cookieStr.indexOf("=");
        if (eqIdx === -1) return null;
        const name = cookieStr.slice(0, eqIdx).trim();
        const rest = cookieStr.slice(eqIdx + 1);
        /* Find where value ends and attributes begin */
        let value = rest;
        /* Value is everything up to first ; that's not inside a {} or "" */
        let depth = 0;
        let inQuote = false;
        for (let i = 0; i < rest.length; i++) {
                const ch = rest[i];
                if (inQuote) {
                        if (ch === '"') inQuote = false;
                        continue;
                }
                if (ch === '"') { inQuote = true; continue; }
                if (ch === '{' || ch === '[') depth++;
                if (ch === '}' || ch === ']') depth--;
                if (ch === ';' && depth === 0) {
                        value = rest.slice(0, i);
                        break;
                }
        }
        /* Parse attributes after the value */
        const attrStr = rest.slice(value.length).trim();
        const attrs = {};
        for (const attrPart of attrStr.split(";")) {
                const attr = attrPart.trim();
                if (!attr) continue;
                const aEq = attr.indexOf("=");
                if (aEq === -1) {
                        attrs[attr.toLowerCase()] = true;
                } else {
                        attrs[attr.slice(0, aEq).trim().toLowerCase()] = attr.slice(aEq + 1).trim();
                }
        }
        return { name, value: value.trim(), attrs };
}

/**
 * Send cookies to the Service Worker for injection via Set-Cookie headers.
 * This is the ONLY way to set HttpOnly / __Secure-* / __Host-* cookies.
 */
function sendCookiesToSW(origin, cookies) {
        if (!navigator.serviceWorker?.controller) {
                console.warn("No SW controller, can't inject cookies via SW");
                return;
        }
        navigator.serviceWorker.controller.postMessage({
                type: "set-cookies",
                origin: origin,
                cookies: cookies,
        });
        console.info(`Sent ${cookies.length} cookies to SW for`, origin);
}

/**
 * Pre-inject stored cookies for a URL into the SW BEFORE navigation.
 * This runs before activeFrame.go(url) so the SW can add Set-Cookie headers
 * on the very first response of the proxied page.
 */
function preInjectCookiesForUrl(url) {
        const profile = getActiveProfile();
        if (!profile || !profile.siteData) return;
        const origin = new URL(url).origin;
        const data = profile.siteData[origin];
        if (!data || !data.cookies) return;

        const cookieParts = parseCookieString(data.cookies);
        const swCookies = [];
        for (const part of cookieParts) {
                const parsed = parseSingleCookie(part);
                if (!parsed || !parsed.name) continue;
                swCookies.push({
                        name: parsed.name,
                        value: parsed.value,
                        path: parsed.attrs?.path || "/",
                        domain: parsed.attrs?.domain || "",
                        secure: !!(parsed.attrs?.secure || parsed.name.startsWith("__Secure-") || parsed.name.startsWith("__Host-")),
                        httpOnly: !!parsed.attrs?.httponly,
                });
        }
        if (swCookies.length > 0) {
                sendCookiesToSW(origin, swCookies);
        }
}

/* ── Restore site data to iframe ── */
function restoreSiteData(url) {
        const profile = getActiveProfile();
        if (!profile || !profile.siteData) return;
        const origin = new URL(url).origin;
        const data = profile.siteData[origin];
        if (!data) return;

        /* Parse all cookies from stored data */
        let allParsed = [];
        if (data.cookies) {
                const cookieParts = parseCookieString(data.cookies);
                for (const part of cookieParts) {
                        const parsed = parseSingleCookie(part);
                        if (parsed && parsed.name) allParsed.push(parsed);
                }
        }

        /* ★ Strategy 1: Send ALL cookies to SW for Set-Cookie header injection ★
           This is the ONLY way to set HttpOnly / __Secure-* / __Host-* cookies.
           The SW will add Set-Cookie headers on proxied responses. */
        const swCookies = allParsed.map(c => ({
                name: c.name,
                value: c.value,
                path: c.attrs?.path || "/",
                domain: c.attrs?.domain || "",
                secure: !!(c.attrs?.secure || c.name.startsWith("__Secure-") || c.name.startsWith("__Host-")),
                httpOnly: !!c.attrs?.httponly,
        }));
        sendCookiesToSW(origin, swCookies);

        /* Strategy 2: Also set non-HttpOnly cookies via document.cookie as fallback */
        try {
                const iframeEl = activeFrame?.element;
                if (!iframeEl) return;
                const iframeDoc = iframeEl.contentDocument;
                const iframeWin = iframeEl.contentWindow;

                if (iframeDoc && data.cookies) {
                        let jsRestored = 0;
                        for (const c of allParsed) {
                                /* Skip cookies that can only be set via SW (HttpOnly, __Secure, __Host) */
                                if (c.attrs?.httponly) continue;
                                if (c.name.startsWith("__Secure-") || c.name.startsWith("__Host-")) continue;
                                try {
                                        iframeDoc.cookie = `${c.name}=${c.value}; path=${c.attrs?.path || "/"}; max-age=31536000; SameSite=Lax`;
                                        jsRestored++;
                                } catch (e) {
                                        console.warn("Cannot set cookie via JS:", c.name, e);
                                }
                        }
                        console.info(`JS-restored ${jsRestored}/${allParsed.length} non-HttpOnly cookies for`, origin);
                }

                /* Restore localStorage — skip app keys and null values */
                if (iframeWin && iframeWin.localStorage && data.storage) {
                        let restored = 0;
                        for (const [key, value] of Object.entries(data.storage)) {
                                if (key.startsWith("mobile-browser:")) continue;
                                if (value === null || value === undefined || value === "null") continue;
                                try {
                                        iframeWin.localStorage.setItem(key, value);
                                        restored++;
                                } catch (e) {
                                        console.warn("Cannot restore localStorage key:", key, e);
                                }
                        }
                        console.info(`Restored ${restored}/${Object.keys(data.storage).length} localStorage items for`, origin);
                }
        } catch (err) {
                console.warn("Cannot restore site data to iframe (JS fallback)", err);
        }
}

/* ── Share profile via link ── */
async function shareProfile(profileId) {
        const profile = profiles.find(p => p.id === profileId);
        if (!profile) return;

        const shareData = {
                name: profile.name,
                favorites: profile.favorites || [],
                settings: profile.settings || {},
                siteData: profile.siteData || {},
        };

        const json = JSON.stringify(shareData);
        const encoded = btoa(unescape(encodeURIComponent(json)));

        const shareUrl = new URL(location.origin + location.pathname);
        shareUrl.searchParams.set("profile", encoded);

        /* Warn: this link is uncontrollable */
        const confirmed = await new Promise((resolve) => {
                const overlay = document.createElement("div");
                overlay.className = "prompt-overlay";
                overlay.innerHTML = `
                        <div class="prompt-box" style="text-align:center">
                                <div class="prompt-message" style="margin-bottom:10px">Внимание!</div>
                                <p style="color:var(--muted);font-size:13px;margin-bottom:14px">Ссылка содержит весь профиль в открытом виде. Вы <strong style="color:#ff9b9b">не сможете</strong> отозвать доступ, ограничить кол-во открытий или просмотреть статистику. Любой, кто получит ссылку — навсегда получит все данные.</p>
                                <p style="color:var(--muted);font-size:12px;margin-bottom:14px">Для управляемого шаринга используйте кнопку <strong>"Поделиться (сервер)"</strong> ↗ в профиле.</p>
                                <div class="prompt-actions">
                                        <button type="button" class="prompt-cancel">Отмена</button>
                                        <button type="button" class="prompt-ok">Всё равно скопировать</button>
                                </div>
                        </div>
                `;
                document.body.appendChild(overlay);
                overlay.querySelector(".prompt-ok").onclick = () => { overlay.remove(); resolve(true); };
                overlay.querySelector(".prompt-cancel").onclick = () => { overlay.remove(); resolve(false); };
                overlay.addEventListener("click", (e) => { if (e.target === overlay) { overlay.remove(); resolve(false); } });
        });
        if (!confirmed) return;

        navigator.clipboard.writeText(shareUrl.toString()).then(() => {
                setStatus("Ссылка скопирована (без контроля)!");
        }).catch(() => {
                const ta = document.createElement("textarea");
                ta.value = shareUrl.toString();
                document.body.appendChild(ta);
                ta.select();
                document.execCommand("copy");
                ta.remove();
                setStatus("Ссылка скопирована (без контроля)!");
        });
}

function importProfileFromUrl() {
        const params = new URLSearchParams(location.search);
        const encoded = params.get("profile");
        if (!encoded) return false;

        try {
                const json = decodeURIComponent(escape(atob(encoded)));
                const data = JSON.parse(json);
                pendingImportProfile = data;
                showImportModal(data);
                return true;
        } catch (err) {
                console.error("Failed to import profile from URL", err);
                return false;
        }
}

/* ── Profile modal ── */
function openProfileModal() {
        renderProfiles();
        profileModal.hidden = false;
}

function closeProfileModal() {
        profileModal.hidden = true;
}

function renderProfiles() {
        profileListEl.textContent = "";

        for (const profile of profiles) {
                const row = document.createElement("div");
                row.className = "profile-row" + (profile.id === activeProfileId ? " active" : "");

                const info = document.createElement("div");
                info.className = "profile-info";

                const nameSpan = document.createElement("span");
                nameSpan.className = "profile-name";
                nameSpan.textContent = profile.name;

                const detailSpan = document.createElement("span");
                detailSpan.className = "profile-detail";
                const favCount = (profile.favorites || []).length;
                const siteCount = Object.keys(profile.siteData || {}).length;
                detailSpan.textContent = `${favCount} закладок`;
                if (siteCount > 0) detailSpan.textContent += ` · ${siteCount} сайтов с данными`;

                info.appendChild(nameSpan);
                info.appendChild(detailSpan);
                row.appendChild(info);

                const actions = document.createElement("div");
                actions.className = "profile-actions";

                if (profile.id !== activeProfileId) {
                        const switchBtn = document.createElement("button");
                        switchBtn.type = "button";
                        switchBtn.className = "profile-btn profile-switch";
                        switchBtn.textContent = "Войти";
                        switchBtn.addEventListener("click", (e) => {
                                e.stopPropagation();
                                switchProfile(profile.id);
                                closeProfileModal();
                                setStatus(`Профиль: ${profile.name}`);
                        });
                        actions.appendChild(switchBtn);
                } else {
                        const activeBadge = document.createElement("span");
                        activeBadge.className = "profile-active-badge";
                        activeBadge.textContent = "Активен";
                        actions.appendChild(activeBadge);
                }

                const renameBtn = document.createElement("button");
                renameBtn.type = "button";
                renameBtn.className = "profile-btn profile-rename";
                renameBtn.textContent = "✎";
                renameBtn.title = "Переименовать";
                renameBtn.addEventListener("click", (e) => {
                        e.stopPropagation();
                        const newName = promptInline("Новое имя профиля:", profile.name, (val) => {
                                if (val && val.trim()) {
                                        renameProfile(profile.id, val.trim());
                                }
                        });
                });
                actions.appendChild(renameBtn);

                const shareBtn = document.createElement("button");
                shareBtn.type = "button";
                shareBtn.className = "profile-btn profile-share";
                shareBtn.textContent = "↗";
                shareBtn.title = "Поделиться (без контроля)";
                shareBtn.addEventListener("click", (e) => {
                        e.stopPropagation();
                        shareProfile(profile.id);
                });
                actions.appendChild(shareBtn);

                const serverShareBtn = document.createElement("button");
                serverShareBtn.type = "button";
                serverShareBtn.className = "profile-btn profile-server-share";
                serverShareBtn.textContent = "⇄";
                serverShareBtn.title = "Поделиться (сервер — с контролем)";
                serverShareBtn.addEventListener("click", (e) => {
                        e.stopPropagation();
                        serverShareProfile(profile.id);
                });
                actions.appendChild(serverShareBtn);

                if (profiles.length > 1) {
                        const delBtn = document.createElement("button");
                        delBtn.type = "button";
                        delBtn.className = "profile-btn profile-delete";
                        delBtn.textContent = "✕";
                        delBtn.title = "Удалить";
                        delBtn.addEventListener("click", (e) => {
                                e.stopPropagation();
                                if (confirm(`Удалить профиль "${profile.name}"?`)) {
                                        deleteProfile(profile.id);
                                }
                        });
                        actions.appendChild(delBtn);
                }

                row.appendChild(actions);
                profileListEl.appendChild(row);
        }

        const ap = getActiveProfile();
        profileActiveName.textContent = ap ? ap.name : "Нет профиля";
}

/* ── Inline prompt (no alert/prompt) ── */
function promptInline(message, defaultValue, callback) {
        const overlay = document.createElement("div");
        overlay.className = "prompt-overlay";
        overlay.innerHTML = `
                <div class="prompt-box">
                        <div class="prompt-message">${message}</div>
                        <input type="text" class="prompt-input" value="${(defaultValue || "").replace(/"/g, "&quot;")}" />
                        <div class="prompt-actions">
                                <button type="button" class="prompt-ok">OK</button>
                                <button type="button" class="prompt-cancel">Отмена</button>
                        </div>
                </div>
        `;
        document.body.appendChild(overlay);

        const input = overlay.querySelector(".prompt-input");
        input.focus();
        input.select();

        const close = (val) => {
                overlay.remove();
                callback(val);
        };

        overlay.querySelector(".prompt-ok").addEventListener("click", () => close(input.value));
        overlay.querySelector(".prompt-cancel").addEventListener("click", () => close(null));
        input.addEventListener("keydown", (e) => {
                if (e.key === "Enter") close(input.value);
                if (e.key === "Escape") close(null);
        });
        overlay.addEventListener("click", (e) => {
                if (e.target === overlay) close(null);
        });
}

/* ── Bookmark modal ── */
function showBookmarkModal(url) {
        pendingBookmarkUrl = url;
        let defaultName;
        try { defaultName = new URL(url).hostname.replace(/^www\./, ""); }
        catch { defaultName = url; }
        bookmarkNameInput.value = defaultName;
        bookmarkUrlPreview.textContent = url;
        bookmarkModal.hidden = false;
        setTimeout(() => {
                bookmarkNameInput.focus();
                bookmarkNameInput.select();
        }, 50);
}

function closeBookmarkModal() {
        bookmarkModal.hidden = true;
        pendingBookmarkUrl = null;
}

function saveBookmarkWithName() {
        const url = pendingBookmarkUrl;
        const name = bookmarkNameInput.value.trim();
        if (!url) return;
        const title = name || (() => { try { return new URL(url).hostname.replace(/^www\./, ""); } catch { return url; } })();
        favorites.unshift({ title, url, addedAt: Date.now() });
        favorites = favorites.slice(0, 60);
        writeJson(FAVORITES_KEY, favorites);
        saveProfile();
        renderFavorites();
        closeBookmarkModal();
        setStatus("В избранном");
        scheduleCollapse();
}

bookmarkSaveBtn?.addEventListener("click", saveBookmarkWithName);
bookmarkNameInput?.addEventListener("keydown", (e) => {
        if (e.key === "Enter") saveBookmarkWithName();
        if (e.key === "Escape") closeBookmarkModal();
});
bookmarkModalClose?.addEventListener("click", closeBookmarkModal);
bookmarkModal?.addEventListener("click", (e) => {
        if (e.target === bookmarkModal) closeBookmarkModal();
});

/* ── Import modal ── */
function showImportModal(data) {
        importInfo.textContent = `Профиль: "${data.name}" (${(data.favorites || []).length} закладок, ${Object.keys(data.siteData || {}).length} сайтов с данными). Импортировать?`;
        importModal.hidden = false;
}

function closeImportModal() {
        importModal.hidden = true;
        pendingImportProfile = null;
}

importAccept?.addEventListener("click", () => {
        if (!pendingImportProfile) return;
        const imported = createProfile(pendingImportProfile.name);
        imported.favorites = pendingImportProfile.favorites || [];
        imported.siteData = pendingImportProfile.siteData || {};
        imported.settings = { ...defaultSettings, ...pendingImportProfile.settings };
        writeJson(PROFILES_KEY, profiles);
        switchProfile(imported.id);
        closeImportModal();
        setStatus(`Профиль "${imported.name}" импортирован!`);

        /* Clean URL — remove both ?profile= and ?share= */
        const cleanUrl = new URL(location.origin + location.pathname);
        const u = new URLSearchParams(location.search).get("u");
        if (u) cleanUrl.searchParams.set("u", u);
        window.history.replaceState({}, "", cleanUrl.toString());
});

importDecline?.addEventListener("click", closeImportModal);
importModal?.addEventListener("click", (e) => {
        if (e.target === importModal) closeImportModal();
});

/* ── Profile modal events ── */
profileBtn?.addEventListener("click", () => {
        toggleSettingsMenu(false);
        openProfileModal();
});
profileModalClose?.addEventListener("click", closeProfileModal);
profileModal?.addEventListener("click", (e) => {
        if (e.target === profileModal) closeProfileModal();
});
profileNewBtn?.addEventListener("click", () => {
        promptInline("Имя нового профиля:", "", (val) => {
                if (val && val.trim()) {
                        const p = createProfile(val.trim());
                        closeProfileModal();
                        setStatus(`Профиль "${p.name}" создан`);
                }
        });
});

/* ── Utility functions ── */
function readJson(key, fallback) {
        try { return JSON.parse(localStorage.getItem(key) || "null") ?? fallback; }
        catch { return fallback; }
}

function writeJson(key, value) {
        localStorage.setItem(key, JSON.stringify(value));
}

function normalizeUrl(value) {
        return search(value, searchEngine.value);
}

function serializeArg(arg) {
        if (arg instanceof Error) return `${arg.name}: ${arg.message}\n${arg.stack || ""}`;
        if (typeof arg === "string") return arg;
        try { return JSON.stringify(arg); }
        catch { return String(arg); }
}

function normalizeLogLevel(level) {
        return Object.hasOwn(LOG_LEVELS, level) ? level : "debug";
}

function shouldShowDebugEntry(entry) {
        if (!settings?.logs) return false;
        const threshold = LOG_LEVELS[normalizeLogLevel(settings.logLevel)];
        const value = LOG_LEVELS[entry?.level] ?? LOG_LEVELS.info;
        return value >= threshold;
}

function filteredDebugEntries() {
        return debugEntries.filter(shouldShowDebugEntry);
}

function appendDebug(level, args) {
        const entry = { ts: new Date().toLocaleTimeString(), level, text: args.map(serializeArg).join(" ") };
        debugEntries.push(entry);
        while (debugEntries.length > MAX_DEBUG_LINES) debugEntries.shift();
        writeJson(DEBUG_LOG_KEY, debugEntries);
        if (shouldShowDebugEntry(entry)) renderDebugLine(entry);
}

function installConsoleCapture() {
        for (const level of ["log", "info", "warn", "error", "debug"]) {
                console[level] = (...args) => {
                        nativeConsole[level](...args);
                        appendDebug(level, args);
                };
        }
        window.addEventListener("error", (event) => appendDebug("error", [event.message, `${event.filename}:${event.lineno}:${event.colno}`, event.error || ""]));
        window.addEventListener("unhandledrejection", (event) => appendDebug("error", ["Unhandled promise rejection", event.reason]));
}

function renderDebugLine(entry) {
        if (!debugLog) return;
        const line = document.createElement("div");
        line.className = `log-${entry.level}`;
        line.textContent = `[${entry.ts}] ${entry.level.toUpperCase()} ${entry.text}`;
        debugLog.prepend(line);
        while (debugLog.childNodes.length > MAX_DEBUG_LINES) debugLog.lastChild.remove();
        debugLog.scrollTop = 0;
}

function renderDebugLog() {
        debugLog.textContent = "";
        for (const entry of filteredDebugEntries().slice(-MAX_DEBUG_LINES)) renderDebugLine(entry);
}

function saveSettings() {
        writeJson(SETTINGS_KEY, settings);
}

function applySettings() {
        settings.barPosition = settings.barPosition === "top" ? "top" : "bottom";
        settings.autohide = settings.autohide !== false;
        settings.loadingAnimation = settings.loadingAnimation !== false;
        settings.logs = settings.logs === true;
        settings.logLevel = normalizeLogLevel(settings.logLevel);
        settings.transport = normalizeTransport(settings.transport);
        document.body.classList.toggle("logs-enabled", settings.logs);
        document.body.classList.toggle("bar-top", settings.barPosition === "top");
        document.body.classList.toggle("bar-bottom", settings.barPosition !== "top");
        document.body.classList.toggle("autohide-off", !settings.autohide);
        debugConsole.hidden = !settings.logs;
        settingLogs.checked = settings.logs;
        if (settingLogLevelRow) settingLogLevelRow.hidden = !settings.logs;
        if (settingLogLevel) settingLogLevel.value = settings.logLevel;
        settingBarPosition.value = settings.barPosition;
        settingAutohide.checked = settings.autohide;
        if (settingLoadingAnimation) settingLoadingAnimation.checked = settings.loadingAnimation;
        if (settingTransport) settingTransport.value = settings.transport;
        if (settingBareTransport) settingBareTransport.value = settings.bareTransport;
        if (settings.logs) renderDebugLog();
        if (!settings.autohide) expandBar({ sticky: true });
        else scheduleCollapse();
}

function showLoading(text = "Котик тянет свободу за кабель…") {
        clearTimeout(loadingTimer);
        if (!settings.loadingAnimation) {
                loader?.stop();
                loadingOverlay.hidden = true;
                appendDebug("info", ["loader:skip", "loading animation disabled"]);
                return;
        }
        loadingOverlay.hidden = false;
        loader?.start({ text });
        appendDebug("info", ["loader:start", text]);
        loadingTimer = setTimeout(() => {
                setStatus("Котик всё ещё держит кабель, ждём первый кадр сайта…");
        }, 8000);
}

function hideLoading() {
        clearTimeout(loadingTimer);
        loader?.stop();
        loadingOverlay.hidden = true;
        appendDebug("info", ["loader:stop"]);
}

function setStatus(text) {
        barStatus.textContent = text;
        clearTimeout(statusTimer);
        statusTimer = setTimeout(() => {
                if (barStatus.textContent === text) barStatus.textContent = "";
        }, 2200);
}

function appShareUrl(url = currentUrl) {
        const appUrl = new URL(location.origin + location.pathname);
        if (url) appUrl.searchParams.set("u", url);
        return appUrl.toString();
}

function setAppUrl(url, mode = "push") {
        const next = url ? appShareUrl(url) : location.origin + location.pathname;
        if (mode === "replace") window.history.replaceState({ u: url || "" }, "", next);
        else window.history.pushState({ u: url || "" }, "", next);
}

function pushHistory(url) {
        if (!url || historyStack[historyStack.length - 1] === url) return;
        historyStack.push(url);
        if (historyStack.length > 80) historyStack = historyStack.slice(-80);
        writeJson(HISTORY_KEY, historyStack);
}

function isFavorite(url = currentUrl) {
        return !!url && favorites.some((item) => item.url === url);
}

function updateControls() {
        address.value = currentUrl || "";
        navFav.disabled = !currentUrl;
        navFav.classList.toggle("active", isFavorite());
        navFav.textContent = isFavorite() ? "★" : "☆";
}

function expandBar({ sticky = false } = {}) {
        browserBar.classList.remove("collapsed");
        browserBar.classList.add("expanded");
        document.body.classList.remove("bar-collapsed");
        document.body.classList.add("bar-expanded");
        if (!sticky) scheduleCollapse();
}

function collapseBar() {
        if (!settings.autohide) return;
        if (!document.body.classList.contains("browsing")) return;
        if (document.activeElement === address) return;
        if (!settingsMenu.hidden) return;
        browserBar.classList.add("collapsed");
        browserBar.classList.remove("expanded");
        document.body.classList.add("bar-collapsed");
        document.body.classList.remove("bar-expanded");
}

function scheduleCollapse() {
        clearTimeout(collapseTimer);
        if (!settings.autohide) return;
        if (!document.body.classList.contains("browsing")) return;
        collapseTimer = setTimeout(collapseBar, COLLAPSE_DELAY);
}

async function waitForControllerOrReady(timeoutMs = 10000) {
        if (navigator.serviceWorker.controller) return;
        const ready = navigator.serviceWorker.ready.then(() => {});
        const controllerChanged = new Promise((resolve) => {
                const onChange = () => {
                        navigator.serviceWorker.removeEventListener("controllerchange", onChange);
                        resolve();
                };
                navigator.serviceWorker.addEventListener("controllerchange", onChange, { once: true });
        });
        const timeout = new Promise((resolve) => setTimeout(resolve, timeoutMs));
        await Promise.race([ready, controllerChanged, timeout]);
}

function withTimeout(promise, timeoutMs, message) {
        let timer;
        const timeout = new Promise((_, reject) => {
                timer = setTimeout(() => reject(new Error(message)), timeoutMs);
        });
        return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function deleteIndexedDB(name, timeoutMs = 2500) {
        return new Promise((resolve) => {
                if (!window.indexedDB) return resolve(false);
                let done = false;
                let timer;
                const finish = (ok) => {
                        if (done) return;
                        done = true;
                        clearTimeout(timer);
                        resolve(ok);
                };
                timer = setTimeout(() => finish(false), timeoutMs);
                const req = indexedDB.deleteDatabase(name);
                req.onsuccess = () => finish(true);
                req.onerror = () => finish(false);
                req.onblocked = () => finish(false);
        });
}

async function initScramjetV1Controller(controller) {
        try {
                await withTimeout(controller.init(), 12000, "Scramjet v1 init timeout");
                return;
        } catch (err) {
                console.warn("Scramjet v1 init failed, resetting IndexedDB and retrying", err);
                await deleteIndexedDB("$scramjet");
                await withTimeout(controller.init(), 18000, "Scramjet v1 init retry timeout");
        }
}


function scramjetV1Config() {
        return {
                prefix: SCRAMJET_V1_PREFIX,
                globals: {
                        wrapfn: "$scramjet$wrap",
                        wrappropertybase: "$scramjet__",
                        wrappropertyfn: "$scramjet$prop",
                        cleanrestfn: "$scramjet$clean",
                        importfn: "$scramjet$import",
                        rewritefn: "$scramjet$rewrite",
                        metafn: "$scramjet$meta",
                        setrealmfn: "$scramjet$setrealm",
                        pushsourcemapfn: "$scramjet$pushsourcemap",
                        trysetfn: "$scramjet$tryset",
                        templocid: "$scramjet$temploc",
                        tempunusedid: "$scramjet$tempunused",
                },
                files: {
                        wasm: "/scramjet-v1/scramjet.wasm.wasm",
                        all: "/scramjet-v1/scramjet.all.js",
                        sync: "/scramjet-v1/scramjet.sync.js",
                },
                flags: {
                        serviceworkers: false,
                        syncxhr: false,
                        strictRewrites: true,
                        rewriterLogs: false,
                        captureErrors: true,
                        cleanErrors: false,
                        scramitize: false,
                        sourcemaps: true,
                        destructureRewrites: false,
                        interceptDownloads: false,
                        allowInvalidJs: true,
                        allowFailedIntercepts: true,
                },
                siteFlags: {},
                codec: {
                        encode: "A=>A?encodeURIComponent(A):A",
                        decode: "A=>A?decodeURIComponent(A):A",
                },
        };
}

async function postScramjetV1ConfigToSW() {
        await waitForControllerOrReady(10000);
        const controller = navigator.serviceWorker.controller;
        if (!controller) throw new Error("Service Worker controller is unavailable for Scramjet v1");
        controller.postMessage({ scramjet$type: "loadConfig", config: scramjetV1Config() });
        await new Promise((resolve) => setTimeout(resolve, 250));
}

async function ensureScramjetV1Transport() {
        if (scramjetV1Ready) return scramjetV1Ready;
        scramjetV1Ready = (async () => {
                await registerSW();
                await waitForControllerOrReady(10000);
                const loader = window.$scramjetLoadController;
                if (typeof loader !== "function") throw new Error("Scramjet v1 bundle is not loaded");
                const { ScramjetController } = loader();
                scramjetV1Controller = new ScramjetController({
                        prefix: SCRAMJET_V1_PREFIX,
                        files: {
                                wasm: "/scramjet-v1/scramjet.wasm.wasm",
                                all: "/scramjet-v1/scramjet.all.js",
                                sync: "/scramjet-v1/scramjet.sync.js",
                        },
                });
                try {
                        await ensureUltravioletBareMux();
                        await postScramjetV1ConfigToSW();
                        // controller.init() can hang on IndexedDB in mixed v1/v2 sessions.
                        // Manual config + BareMux transport are enough for createFrame()/go() and SW routing.
                } catch (err) {
                        scramjetV1Ready = null;
                        scramjetV1Controller = null;
                        throw err;
                }
                console.info("Scramjet v1 controller ready");
                return scramjetV1Controller;
        })();
        return scramjetV1Ready;
}

async function ensureUltravioletBareMux() {
        if (ultravioletBareMuxReady) return ultravioletBareMuxReady;
        ultravioletBareMuxReady = (async () => {
                const wispUrl = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/wisp/`;
                const bareTransport = selectedBareTransport();
                const connection = new BareMuxConnection("/baremux/worker.js");
                await connection.setTransport(bareTransportModulePath(bareTransport), [{ wisp: wispUrl }]);
                console.info("Ultraviolet BareMux transport ready", { wispUrl, bareTransport: bareTransportLabel(bareTransport) });
                return connection;
        })();
        return ultravioletBareMuxReady;
}

async function waitForServiceWorkerActivation(registration) {
        const worker = registration.active || registration.waiting || registration.installing;
        if (!worker) return registration;
        if (worker.state === "activated") return registration;
        await new Promise((resolve, reject) => {
                const timeout = setTimeout(() => reject(new Error("Service worker activation timeout")), 10000);
                worker.addEventListener("statechange", () => {
                        if (worker.state === "activated") {
                                clearTimeout(timeout);
                                resolve();
                        }
                });
        });
        return registration;
}

async function ensureUltravioletTransport() {
        if (ultravioletReady) return ultravioletReady;
        ultravioletReady = (async () => {
                if (!window.__uv$config || !window.Ultraviolet) throw new Error("Ultraviolet bundle/config is not loaded");
                await ensureUltravioletBareMux();
                const registration = await navigator.serviceWorker.register("/uv/sw.js?v=20260531_sharedworker1", { scope: "/uv/" });
                if (registration.waiting) registration.waiting.postMessage({ type: "SKIP_WAITING" });
                await waitForServiceWorkerActivation(registration);
                console.info("Ultraviolet service worker ready", window.__uv$config.prefix);
                return registration;
        })();
        return ultravioletReady;
}

async function ensureTransport() {
        if (controllerReady) return controllerReady;
        controllerReady = (async () => {
                try { await registerSW(); }
                catch (err) {
                        error.textContent = "Failed to register service worker.";
                        errorCode.textContent = err.toString();
                        console.error("service worker registration failed", err);
                        throw err;
                }
                await waitForControllerOrReady(10000);
                const registration = await navigator.serviceWorker.getRegistration("/");
                const readySw = navigator.serviceWorker.controller || registration?.active || registration?.waiting || registration?.installing;
                if (!readySw) {
                        console.error("service worker registration missing", { controller: navigator.serviceWorker.controller, registration });
                        throw new Error("No service worker available for Scramjet controller");
                }
                installWhiteTransportIfAvailable();
                const wispUrl = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/wisp/`;
                console.info("initializing Scramjet v2 controller", wispUrl);
                controller = new Controller({
                        serviceworker: readySw,
                        transport: new LibcurlClient({ wisp: wispUrl }),
                        scramjetConfig: defaultConfigDev,
                });
                await controller.wait();
                console.info("Scramjet v2 controller ready");
                return controller;
        })();
        return controllerReady;
}

function resetActiveFrame(reason = "") {
        if (!activeFrame) return;
        console.info("reset Scramjet frame", reason);
        try {
                if (controller?.frames) controller.frames = controller.frames.filter((frame) => frame !== activeFrame);
        } catch {}
        try { activeFrame.element?.remove?.(); } catch {}
        activeFrame = null;
}

function detectScramjetRouteError() {
        if (!activeFrame?.element) return false;
        try {
                const doc = activeFrame.element.contentDocument;
                if (!doc) return false;
                const title = doc.title || "";
                const text = doc.body?.innerText || "";
                return /Scramjet\s*\|\s*Error/i.test(title) && /could not route|No frame found|route your request/i.test(text);
        } catch {
                return false;
        }
}

function scheduleRouteErrorRetry(url) {
        if (!url || routeErrorRetryUrl === url) return;
        routeErrorRetryUrl = url;
        clearTimeout(routeErrorRetryTimer);
        routeErrorRetryTimer = setTimeout(async () => {
                if (!detectScramjetRouteError()) return;
                console.warn("Scramjet route error detected, retrying with a fresh frame", url);
                resetActiveFrame("route-error-retry");
                try {
                        await ensureTransport();
                        const frame = ensureFrame();
                        preInjectCookiesForUrl(url);
                        frame.go(url);
                } catch (err) {
                        console.error("route error retry failed", err);
                }
        }, 250);
}


/* ── Remote render transport: server Chrome -> compressed canvas tiles ── */
function closeRemoteBrowser() {
        if (!remoteBrowser) return;
        console.info("remote-browser close");
        try { remoteBrowser.ws?.close(); } catch {}
        try { remoteBrowser.root?.remove(); } catch {}
        remoteBrowser = null;
}

function collectRemoteDeviceProfile() {
        const rect = stage.getBoundingClientRect();
        const vv = window.visualViewport;
        const orientation = screen.orientation || {};
        const viewportWidth = Math.max(240, Math.round(rect.width || vv?.width || innerWidth || screen.width || 390));
        const viewportHeight = Math.max(240, Math.round(rect.height || vv?.height || innerHeight || screen.height || 844));
        let timezone = "";
        try { timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch {}
        return {
                viewportWidth,
                viewportHeight,
                devicePixelRatio: Number(window.devicePixelRatio || 1),
                screenWidth: Math.round(screen.width || viewportWidth),
                screenHeight: Math.round(screen.height || viewportHeight),
                availWidth: Math.round(screen.availWidth || screen.width || viewportWidth),
                availHeight: Math.round(screen.availHeight || screen.height || viewportHeight),
                visualViewportWidth: Math.round(vv?.width || viewportWidth),
                visualViewportHeight: Math.round(vv?.height || viewportHeight),
                visualViewportScale: Number(vv?.scale || 1),
                visualViewportOffsetLeft: Math.round(vv?.offsetLeft || 0),
                visualViewportOffsetTop: Math.round(vv?.offsetTop || 0),
                userAgent: navigator.userAgent || "",
                platform: navigator.platform || "",
                language: navigator.language || "",
                timezone,
                maxTouchPoints: navigator.maxTouchPoints || 1,
                touch: navigator.maxTouchPoints > 0 || "ontouchstart" in window,
                mobile: /Mobile|iPhone|iPad|Android/i.test(navigator.userAgent || ""),
                orientationType: orientation.type || (viewportHeight >= viewportWidth ? "portrait-primary" : "landscape-primary"),
                orientationAngle: Number(orientation.angle || 0),
        };
}

function encodeRemoteDeviceProfile(profile) {
        const bytes = new TextEncoder().encode(JSON.stringify(profile));
        let binary = "";
        for (const byte of bytes) binary += String.fromCharCode(byte);
        return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function remoteCanvasPoint(event) {
        const rect = remoteBrowser.canvas.getBoundingClientRect();
        const scaleX = remoteBrowser.inputWidth / Math.max(1, rect.width);
        const scaleY = remoteBrowser.inputHeight / Math.max(1, rect.height);
        return {
                x: Math.max(0, Math.min(remoteBrowser.inputWidth, Math.round((event.clientX - rect.left) * scaleX))),
                y: Math.max(0, Math.min(remoteBrowser.inputHeight, Math.round((event.clientY - rect.top) * scaleY))),
                inside: event.clientX >= rect.left && event.clientX <= rect.right && event.clientY >= rect.top && event.clientY <= rect.bottom,
        };
}

function updateRemoteCanvasLayout() {
        if (!remoteBrowser?.root || !remoteBrowser?.canvas) return;
        const rootRect = remoteBrowser.root.getBoundingClientRect();
        const frameWidth = remoteBrowser.frameWidth || remoteBrowser.canvas.width || remoteBrowser.inputWidth || 1;
        const frameHeight = remoteBrowser.frameHeight || remoteBrowser.canvas.height || remoteBrowser.inputHeight || 1;
        const rootWidth = Math.max(1, rootRect.width);
        const rootHeight = Math.max(1, rootRect.height);
        const scale = Math.min(rootWidth / frameWidth, rootHeight / frameHeight);
        const cssWidth = Math.max(1, frameWidth * scale);
        const cssHeight = Math.max(1, frameHeight * scale);
        const left = (rootWidth - cssWidth) / 2;
        const top = (rootHeight - cssHeight) / 2;
        Object.assign(remoteBrowser.canvas.style, {
                width: `${cssWidth}px`,
                height: `${cssHeight}px`,
                left: `${left}px`,
                top: `${top}px`,
        });
        remoteBrowser.displayRect = { x: left, y: top, width: cssWidth, height: cssHeight, scale };
}

function remoteSend(payload) {
        if (!remoteBrowser?.ws || remoteBrowser.ws.readyState !== WebSocket.OPEN) return;
        remoteBrowser.ws.send(JSON.stringify(payload));
}

function sendRemoteDeviceProfile() {
        if (!remoteBrowser) return null;
        const profile = collectRemoteDeviceProfile();
        remoteBrowser.deviceProfile = profile;
        remoteBrowser.inputWidth = profile.viewportWidth;
        remoteBrowser.inputHeight = profile.viewportHeight;
        if (!remoteBrowser.frameWidth) {
                remoteBrowser.canvas.width = profile.viewportWidth;
                remoteBrowser.canvas.height = profile.viewportHeight;
        }
        remoteSend({ type: "device", profile });
        return profile;
}

function sendRemoteResize() {
        sendRemoteDeviceProfile();
        updateRemoteCanvasLayout();
}

function drawRemoteFrame(frame) {
        if (!remoteBrowser) return;
        const ctx = remoteBrowser.ctx;
        remoteBrowser.frameWidth = frame.width || remoteBrowser.frameWidth || remoteBrowser.inputWidth;
        remoteBrowser.frameHeight = frame.height || remoteBrowser.frameHeight || remoteBrowser.inputHeight;
        remoteBrowser.inputWidth = frame.viewportWidth || remoteBrowser.inputWidth || remoteBrowser.frameWidth;
        remoteBrowser.inputHeight = frame.viewportHeight || remoteBrowser.inputHeight || remoteBrowser.frameHeight;
        remoteBrowser.dpr = frame.dpr || remoteBrowser.dpr;
        if (remoteBrowser.canvas.width !== remoteBrowser.frameWidth) remoteBrowser.canvas.width = remoteBrowser.frameWidth;
        if (remoteBrowser.canvas.height !== remoteBrowser.frameHeight) remoteBrowser.canvas.height = remoteBrowser.frameHeight;
        updateRemoteCanvasLayout();
        remoteBrowser.frames = (remoteBrowser.frames || 0) + 1;
        if (frame.image) {
                const image = new Image();
                image.onload = () => {
                        ctx.drawImage(image, 0, 0, remoteBrowser.frameWidth, remoteBrowser.frameHeight);
                        if (remoteBrowser.frames === 1) hideLoading();
                };
                image.src = `data:image/jpeg;base64,${frame.image}`;
        } else {
                if (frame.key) ctx.clearRect(0, 0, remoteBrowser.canvas.width, remoteBrowser.canvas.height);
                for (const tile of frame.tiles || []) {
                        const image = new Image();
                        image.onload = () => ctx.drawImage(image, tile.x, tile.y, tile.w, tile.h);
                        image.src = `data:image/jpeg;base64,${tile.data}`;
                }
                if (remoteBrowser.frames === 1) hideLoading();
        }
        const mode = frame.mode === "screencast" ? (frame.lowLatency ? "low-latency" : "stream") : `tiles ${frame.tiles?.length || 0}`;
        const dropped = frame.dropped ? ` · dropped ${frame.dropped}` : "";
        setStatus(`${transportLabel(TRANSPORT_RENDER)} · frame ${frame.frameId} · ${mode}${dropped}`);
}

function buildRemoteBrowserStage(url) {
        closeRemoteBrowser();
        resetActiveFrame("remote-render");
        stage.textContent = "";
        stage.hidden = false;
        document.body.classList.add("browsing", "bar-expanded");
        document.body.classList.remove("bar-collapsed");
        expandBar();

        const root = document.createElement("div");
        root.className = "remote-browser";
        const canvas = document.createElement("canvas");
        canvas.className = "remote-browser-canvas";
        canvas.tabIndex = 0;
        canvas.setAttribute("aria-label", "Серверный браузер");
        const hud = document.createElement("div");
        hud.className = "remote-browser-hud";
        hud.textContent = "server render";
        const keyboard = document.createElement("textarea");
        keyboard.className = "remote-browser-keyboard";
        keyboard.rows = 1;
        keyboard.placeholder = "⌨ текст → сервер";
        keyboard.autocapitalize = "off";
        keyboard.autocomplete = "off";
        keyboard.spellcheck = false;
        root.append(canvas, hud, keyboard);
        stage.appendChild(root);

        const ctx = canvas.getContext("2d", { alpha: false });
        const profile = collectRemoteDeviceProfile();
        remoteBrowser = { root, canvas, ctx, hud, keyboard, deviceProfile: profile, inputWidth: profile.viewportWidth, inputHeight: profile.viewportHeight, dpr: profile.devicePixelRatio, lastKeyboardValue: "" };
        canvas.width = profile.viewportWidth;
        canvas.height = profile.viewportHeight;
        window.remoteBrowser = remoteBrowser;
        updateRemoteCanvasLayout();

        canvas.addEventListener("pointerdown", (event) => {
                event.preventDefault();
                canvas.focus({ preventScroll: true });
                canvas.setPointerCapture?.(event.pointerId);
                const p = remoteCanvasPoint(event);
                remoteSend({ type: "pointer", action: "down", x: p.x, y: p.y, pointerId: event.pointerId, pointerType: event.pointerType, pressure: event.pressure });
        });
        canvas.addEventListener("pointermove", (event) => {
                if (!event.buttons && event.pointerType !== "touch") return;
                event.preventDefault();
                const p = remoteCanvasPoint(event);
                remoteSend({ type: "pointer", action: "move", x: p.x, y: p.y, pointerId: event.pointerId, pointerType: event.pointerType, pressure: event.pressure });
        });
        for (const name of ["pointerup", "pointercancel"]) {
                canvas.addEventListener(name, (event) => {
                        event.preventDefault();
                        const p = remoteCanvasPoint(event);
                        remoteSend({ type: "pointer", action: name === "pointerup" ? "up" : "cancel", x: p.x, y: p.y, pointerId: event.pointerId, pointerType: event.pointerType });
                });
        }
        canvas.addEventListener("wheel", (event) => {
                event.preventDefault();
                const p = remoteCanvasPoint(event);
                remoteSend({ type: "wheel", x: p.x, y: p.y, deltaX: event.deltaX, deltaY: event.deltaY });
        }, { passive: false });
        canvas.addEventListener("keydown", (event) => {
                if (event.metaKey || event.ctrlKey || event.altKey) return;
                if (event.key.length === 1) remoteSend({ type: "text", text: event.key });
                else remoteSend({ type: "key", action: "down", key: event.key, code: event.code });
                if (["Backspace", "Enter", "Tab", "Escape", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"].includes(event.key)) event.preventDefault();
        });
        keyboard.addEventListener("input", () => {
                const prev = remoteBrowser.lastKeyboardValue || "";
                const next = keyboard.value || "";
                if (next.length > prev.length && next.startsWith(prev)) {
                        remoteSend({ type: "text", text: next.slice(prev.length) });
                } else if (next.length < prev.length) {
                        remoteSend({ type: "key", action: "down", key: "Backspace", code: "Backspace" });
                } else if (next !== prev) {
                        remoteSend({ type: "text", text: next });
                }
                remoteBrowser.lastKeyboardValue = next;
        });
        keyboard.addEventListener("keydown", (event) => {
                if (event.key === "Enter") {
                        remoteSend({ type: "key", action: "down", key: "Enter", code: "Enter" });
                        keyboard.value = "";
                        remoteBrowser.lastKeyboardValue = "";
                        event.preventDefault();
                }
        });
        addEventListener("resize", sendRemoteResize, { passive: true });
        return remoteBrowser;
}

async function openRemoteBrowser(url) {
        const rb = buildRemoteBrowserStage(url);
        showLoading("Серверный Chrome рисует первый кадр…");
        const deviceParam = encodeURIComponent(encodeRemoteDeviceProfile(rb.deviceProfile || collectRemoteDeviceProfile()));
        const wsUrl = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/remote-browser/ws?u=${encodeURIComponent(url)}&device=${deviceParam}`;
        const ws = new WebSocket(wsUrl);
        rb.ws = ws;
        ws.addEventListener("open", () => {
                console.info("remote-browser ws open", url, rb.deviceProfile);
                sendRemoteDeviceProfile();
        });
        ws.addEventListener("message", (event) => {
                let msg;
                try { msg = JSON.parse(event.data); }
                catch { return; }
                if (msg.type === "frame") drawRemoteFrame(msg);
                else if (msg.type === "status") {
                        console.info("[remote-browser]", msg);
                        rb.hud.textContent = `${msg.message || "remote"}${msg.dpr ? ` · dpr ${msg.dpr}` : ""}${msg.proxy ? ` · ${msg.proxy}` : ""}`;
                } else if (msg.type === "url" && msg.url) {
                        currentUrl = msg.url;
                        updateControls();
                } else if (msg.type === "log") {
                        console[msg.level || "info"]("[remote-browser]", msg.text);
                }
        });
        ws.addEventListener("close", () => {
                hideLoading();
                console.warn("remote-browser ws closed");
                setStatus("Серверный браузер отключён");
        });
        ws.addEventListener("error", (event) => {
                hideLoading();
                console.error("remote-browser ws error", event);
                setStatus("Ошибка server render");
        });
}

async function openScramjetV1Frame(url) {
        closeRemoteBrowser();
        resetActiveFrame("scramjet-v1-navigation");
        await ensureScramjetV1Transport();
        const scramjetFrame = scramjetV1Controller.createFrame();
        const frameElement = scramjetFrame.frame;
        frameElement.id = "sj-frame";
        enableCredentiallessIframe(frameElement);
        activeFrame = {
                element: frameElement,
                frame: frameElement,
                raw: scramjetFrame,
                go: (nextUrl) => scramjetFrame.go(nextUrl),
                reload: () => scramjetFrame.reload(),
                back: () => scramjetFrame.back(),
                forward: () => scramjetFrame.forward(),
        };
        if (!frameElement.parentNode) stage.appendChild(frameElement);
        frameElement.addEventListener("load", () => {
                hideLoading();
                console.info("scramjet v1 iframe load", url, frameElement.src);
                updateControls();
                scheduleCollapse();
        });
        stage.hidden = false;
        document.body.classList.add("browsing", "bar-expanded");
        document.body.classList.remove("bar-collapsed");
        expandBar();
        setStatus(`${transportLabel(TRANSPORT_SCRAMJET_V1)} · ${new URL(url).hostname}`);
        activeFrame.go(url);
}

async function openUltravioletFrame(url) {
        closeRemoteBrowser();
        resetActiveFrame("ultraviolet-navigation");
        await ensureUltravioletTransport();
        const iframe = document.createElement("iframe");
        iframe.id = "sj-frame";
        iframe.referrerPolicy = "no-referrer";
        enableCredentiallessIframe(iframe);
        iframe.src = ultravioletProxyUrl(url);
        activeFrame = { element: iframe };
        stage.appendChild(iframe);
        iframe.addEventListener("load", () => {
                hideLoading();
                console.info("ultraviolet iframe load", url);
                updateControls();
                scheduleCollapse();
        });
        stage.hidden = false;
        document.body.classList.add("browsing", "bar-expanded");
        document.body.classList.remove("bar-collapsed");
        expandBar();
        setStatus(`${transportLabel(TRANSPORT_ULTRAVIOLET)} · ${new URL(url).hostname}`);
}

function openAntiCorsProxyFrame(url) {
        closeRemoteBrowser();
        resetActiveFrame("anticors-navigation");
        const iframe = document.createElement("iframe");
        iframe.id = "sj-frame";
        iframe.referrerPolicy = "no-referrer";
        enableCredentiallessIframe(iframe);
        iframe.src = antiCorsProxyUrl(url);
        activeFrame = { element: iframe };
        stage.appendChild(iframe);
        iframe.addEventListener("load", () => {
                hideLoading();
                console.info("anticors iframe load", url);
                updateControls();
                scheduleCollapse();
        });
        stage.hidden = false;
        document.body.classList.add("browsing", "bar-expanded");
        document.body.classList.remove("bar-collapsed");
        expandBar();
        setStatus(`${transportLabel(TRANSPORT_ANTICORS)} · ${new URL(url).hostname}`);
}

function ensureFrame() {
        if (!activeFrame) {
                if (!controller) throw new Error("Scramjet controller is not ready");
                const iframe = document.createElement("iframe");
                iframe.id = "sj-frame";
                activeFrame = controller.createFrame(iframe);
                stage.appendChild(activeFrame.element);
                activeFrame.element.addEventListener("load", () => {
                        hideLoading();
                        console.info("iframe load", currentUrl);
                        scheduleRouteErrorRetry(currentUrl);
                        /* Restore site data after navigation */
                        if (currentUrl) {
                                setTimeout(() => restoreSiteData(currentUrl), 500);
                        }
                        updateControls();
                        scheduleCollapse();
                });
        }
        stage.hidden = false;
        document.body.classList.add("browsing", "bar-expanded");
        document.body.classList.remove("bar-collapsed");
        expandBar();
        return activeFrame;
}

async function openAddress(value, { addHistory = true, urlMode = "push" } = {}) {
        const url = normalizeUrl(value);
        currentUrl = url;
        routeErrorRetryUrl = "";
        clearTimeout(routeErrorRetryTimer);
        console.info("open", url);
        if (addHistory) pushHistory(url);
        updateControls();
        setAppUrl(url, urlMode);
        showLoading();
        if (isRemoteRenderEnabled()) {
                await openRemoteBrowser(url);
                scheduleCollapse();
                return;
        }
        if (isAntiCorsProxyEnabled()) {
                openAntiCorsProxyFrame(url);
                scheduleCollapse();
                return;
        }
        if (isScramjetV1Enabled()) {
                try {
                        await openScramjetV1Frame(url);
                        scheduleCollapse();
                } catch (err) {
                        hideLoading();
                        resetActiveFrame("scramjet-v1-failed");
                        console.error("Scramjet v1 failed", err);
                        setStatus("Scramjet v1 не запустился — попробуй Ultraviolet или Серверный браузер");
                }
                return;
        }
        if (isUltravioletEnabled()) {
                await openUltravioletFrame(url);
                scheduleCollapse();
                return;
        }
        closeRemoteBrowser();
        try {
                await ensureTransport();
                resetActiveFrame("new-navigation");
                ensureFrame();

                /* ★ Pre-inject cookies into SW BEFORE navigation ★
                   This ensures the SW can add Set-Cookie headers on the proxied response
                   for the initial page load. */
                preInjectCookiesForUrl(url);

                activeFrame.go(url);
        } catch (err) {
                hideLoading();
                throw err;
        }
        scheduleCollapse();
}

function renderFavorites() {
        favoritesList.textContent = "";
        const hasFavorites = favorites.length > 0;
        favoritesSection.hidden = !hasFavorites;
        if (heroCard) heroCard.hidden = hasFavorites;
        for (const item of favorites) {
                const wrapper = document.createElement("div");
                wrapper.className = "fav-wrapper";

                const button = document.createElement("button");
                button.type = "button";
                button.className = "fav-link";
                button.dataset.openUrl = item.url;
                button.textContent = item.title || item.url;
                button.title = item.url;

                const delBtn = document.createElement("button");
                delBtn.type = "button";
                delBtn.className = "fav-del";
                delBtn.textContent = "✕";
                delBtn.title = "Удалить";
                delBtn.addEventListener("click", (e) => {
                        e.stopPropagation();
                        const idx = favorites.findIndex(f => f.url === item.url);
                        if (idx >= 0) {
                                favorites.splice(idx, 1);
                                writeJson(FAVORITES_KEY, favorites);
                                saveProfile();
                                renderFavorites();
                                setStatus("Удалено");
                        }
                });

                wrapper.appendChild(button);
                wrapper.appendChild(delBtn);
                favoritesList.appendChild(wrapper);
        }
        updateControls();
}

function toggleFavorite() {
        if (!currentUrl) return;
        const index = favorites.findIndex((item) => item.url === currentUrl);
        if (index >= 0) {
                favorites.splice(index, 1);
                writeJson(FAVORITES_KEY, favorites);
                saveProfile();
                renderFavorites();
                setStatus("Удалено");
                scheduleCollapse();
        } else {
                /* Show name input modal */
                showBookmarkModal(currentUrl);
        }
}

function goHome({ replace = false } = {}) {
        hideLoading();
        closeRemoteBrowser();
        stage.hidden = true;
        document.body.classList.remove("browsing", "bar-collapsed", "bar-expanded");
        browserBar.classList.remove("collapsed");
        browserBar.classList.add("expanded");
        currentUrl = "";
        updateControls();
        if (replace) setAppUrl("", "replace");
}

function toggleSettingsMenu(force) {
        const open = force ?? settingsMenu.hidden;
        settingsMenu.hidden = !open;
        browserBar.classList.toggle("menu-open", open);
        if (open) expandBar({ sticky: true });
        else scheduleCollapse();
}

/* ── Server share modal refs ── */
const serverShareModal = document.getElementById("server-share-modal");
const serverShareClose = document.getElementById("server-share-close");
const serverShareResult = document.getElementById("server-share-result");
const serverShareLoading = document.getElementById("server-share-loading");
const serverShareUrl = document.getElementById("server-share-url");
const serverShareOwnerKey = document.getElementById("server-share-owner-key");
const serverShareStats = document.getElementById("server-share-stats");
const serverShareCopy = document.getElementById("server-share-copy");
const serverShareCopyKey = document.getElementById("server-share-copy-key");
const serverShareRevoke = document.getElementById("server-share-revoke");

let currentShareToken = null;
let currentShareOwnerKey = null;
let shareStatsInterval = null;

/* ── Server share logic ── */
async function serverShareProfile(profileId) {
        const profile = profiles.find(p => p.id === profileId);
        if (!profile) return;

        /* Save profile before sharing */
        saveProfile();

        currentShareToken = null;
        currentShareOwnerKey = null;

        serverShareUrl.textContent = "";
        serverShareOwnerKey.textContent = "";
        serverShareStats.textContent = "";
        serverShareResult.hidden = true;
        serverShareLoading.hidden = false;
        serverShareModal.hidden = false;
        clearInterval(shareStatsInterval);

        try {
                const res = await fetch("/api/share", {
                        method: "POST",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({
                                profile: {
                                        name: profile.name,
                                        favorites: profile.favorites || [],
                                        settings: profile.settings || {},
                                        siteData: profile.siteData || {},
                                },
                        }),
                });
                if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        throw new Error(err.error || `HTTP ${res.status}`);
                }
                const data = await res.json();
                currentShareToken = data.token;
                currentShareOwnerKey = data.ownerKey;

                serverShareUrl.textContent = data.url;
                serverShareOwnerKey.textContent = data.ownerKey;
                serverShareLoading.hidden = true;
                serverShareResult.hidden = false;
                updateServerShareStats(data);

                /* Auto-refresh stats every 5s */
                shareStatsInterval = setInterval(refreshServerShareStats, 5000);

                console.info("Server share created", data.token);
        } catch (err) {
                serverShareLoading.hidden = true;
                serverShareModal.hidden = true;
                setStatus("Ошибка: " + err.message);
                console.error("Server share failed", err);
        }
}

async function refreshServerShareStats() {
        if (!currentShareToken || !currentShareOwnerKey) return;
        try {
                const res = await fetch(`/api/share/${currentShareToken}/stats?owner=${encodeURIComponent(currentShareOwnerKey)}`);
                if (!res.ok) return;
                const data = await res.json();
                updateServerShareStats(data);
        } catch {
                /* silent */
        }
}

function updateServerShareStats(data) {
        const views = data.views || 0;
        const created = data.createdAt ? new Date(data.createdAt).toLocaleString("ru") : "—";
        const lastView = data.lastViewAt ? new Date(data.lastViewAt).toLocaleString("ru") : "—";

        if (data.revoked) {
                serverShareStats.innerHTML = `
                        <div class="share-stat-revoked">Доступ отозван</div>
                        <div class="share-stat-row"><span>Открытий:</span> <strong>${views}</strong></div>
                        <div class="share-stat-row"><span>Создан:</span> <strong>${created}</strong></div>
                        <div class="share-stat-row"><span>Последнее открытие:</span> <strong>${lastView}</strong></div>
                `;
                serverShareRevoke.textContent = "Уже отозвано";
                serverShareRevoke.disabled = true;
        } else {
                serverShareStats.innerHTML = `
                        <div class="share-stat-row"><span>Открытий:</span> <strong>${views}</strong></div>
                        <div class="share-stat-row"><span>Создан:</span> <strong>${created}</strong></div>
                        <div class="share-stat-row"><span>Последнее открытие:</span> <strong>${lastView}</strong></div>
                `;
        }
}

function closeServerShareModal() {
        serverShareModal.hidden = true;
        clearInterval(shareStatsInterval);
        currentShareToken = null;
        currentShareOwnerKey = null;
}

async function revokeServerShare() {
        if (!currentShareToken || !currentShareOwnerKey) return;
        try {
                const res = await fetch(`/api/share/${currentShareToken}`, {
                        method: "DELETE",
                        headers: { "Content-Type": "application/json" },
                        body: JSON.stringify({ ownerKey: currentShareOwnerKey }),
                });
                if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        throw new Error(err.error || `HTTP ${res.status}`);
                }
                setStatus("Доступ отозван!");
                refreshServerShareStats();
        } catch (err) {
                setStatus("Ошибка отзыва: " + err.message);
                console.error("Revoke failed", err);
        }
}

async function importServerShare(token) {
        try {
                const res = await fetch(`/api/share/${token}`);
                if (!res.ok) {
                        const err = await res.json().catch(() => ({}));
                        if (res.status === 410) {
                                showImportModal({ name: "Отозван", favorites: [], settings: {}, siteData: {} });
                                importInfo.textContent = "Профиль был отозван владельцем.";
                                importAccept.hidden = true;
                                return;
                        }
                        throw new Error(err.error || `HTTP ${res.status}`);
                }
                const data = await res.json();
                pendingImportProfile = data;
                showImportModal(data);
        } catch (err) {
                console.error("Import from server failed", err);
                setStatus("Ошибка загрузки профиля");
        }
}

/* Server share modal events */
serverShareClose?.addEventListener("click", closeServerShareModal);
serverShareModal?.addEventListener("click", (e) => {
        if (e.target === serverShareModal) closeServerShareModal();
});
serverShareCopy?.addEventListener("click", async () => {
        try { await navigator.clipboard.writeText(serverShareUrl.textContent); setStatus("Ссылка скопирована!"); }
        catch { setStatus("Не удалось скопировать"); }
});
serverShareCopyKey?.addEventListener("click", async () => {
        try { await navigator.clipboard.writeText(serverShareOwnerKey.textContent); setStatus("Ключ скопирован!"); }
        catch { setStatus("Не удалось скопировать"); }
});
serverShareRevoke?.addEventListener("click", revokeServerShare);

/* ── Capture site data button ── */
const captureBtn = document.getElementById("setting-capture");
captureBtn?.addEventListener("click", () => {
        toggleSettingsMenu(false);
        captureSiteData();
});

/* ── Event listeners ── */
form.addEventListener("submit", async (event) => {
        event.preventDefault();
        const value = address.value.trim();
        if (!value) return goHome();
        await openAddress(value);
});

document.addEventListener("click", async (event) => {
        const button = event.target.closest("[data-open-url]");
        if (button) return openAddress(button.dataset.openUrl);
        if (!event.target.closest(".browser-bar") && !event.target.closest(".modal-overlay") && !event.target.closest(".prompt-overlay")) {
                toggleSettingsMenu(false);
        }
});

navMenu.addEventListener("click", (event) => {
        event.preventDefault();
        toggleSettingsMenu();
});
navFav.addEventListener("click", toggleFavorite);
settingHome.addEventListener("click", () => {
        toggleSettingsMenu(false);
        goHome();
});
settingLogs.addEventListener("change", () => {
        settings.logs = settingLogs.checked;
        if (settings.logs && !settings.logLevel) settings.logLevel = "debug";
        saveSettings();
        saveProfile();
        applySettings();
});
settingLogLevel?.addEventListener("change", () => {
        settings.logLevel = normalizeLogLevel(settingLogLevel.value);
        saveSettings();
        saveProfile();
        applySettings();
});
settingBarPosition.addEventListener("change", () => {
        settings.barPosition = settingBarPosition.value;
        saveSettings();
        saveProfile();
        applySettings();
});
settingAutohide.addEventListener("change", () => {
        settings.autohide = settingAutohide.checked;
        saveSettings();
        saveProfile();
        applySettings();
});
settingLoadingAnimation?.addEventListener("change", () => {
        settings.loadingAnimation = settingLoadingAnimation.checked;
        saveSettings();
        saveProfile();
        applySettings();
        if (!settings.loadingAnimation) hideLoading();
});
settingTransport?.addEventListener("change", () => {
        const previous = selectedTransport();
        settings.transport = normalizeTransport(settingTransport.value);
        saveSettings();
        saveProfile();
        applySettings();
        setStatus(`Транспорт: ${transportLabel(settings.transport)}`);
        if (controllerReady && settings.transport !== previous) {
                setStatus("Транспорт изменён, перезагружаю…");
                setTimeout(() => window.location.reload(), 500);
        }
});
settingBareTransport?.addEventListener("change", () => {
        const previous = selectedBareTransport();
        settings.bareTransport = normalizeBareTransport(settingBareTransport.value);
        saveSettings();
        saveProfile();
        applySettings();
        setStatus(`BareMux: ${bareTransportLabel(settings.bareTransport)}`);
        if (controllerReady && settings.bareTransport !== previous) {
                setStatus("BareMux transport изменён, перезагружаю…");
                setTimeout(() => window.location.reload(), 500);
        }
});
debugCopy.addEventListener("click", async () => {
        const text = filteredDebugEntries().slice().reverse().map((entry) => `[${entry.ts}] ${entry.level.toUpperCase()} ${entry.text}`).join("\n");
        try { await navigator.clipboard.writeText(text); setStatus("Консоль скопирована"); }
        catch { setStatus("Не удалось скопировать"); }
});
debugClear.addEventListener("click", () => {
        debugEntries.splice(0, debugEntries.length);
        writeJson(DEBUG_LOG_KEY, debugEntries);
        renderDebugLog();
});
barPeek.addEventListener("click", () => expandBar());
browserBar.addEventListener("pointerdown", () => {
        if (browserBar.classList.contains("collapsed")) expandBar();
});
browserBar.addEventListener("pointermove", scheduleCollapse);
address.addEventListener("focus", () => expandBar({ sticky: true }));
address.addEventListener("blur", scheduleCollapse);

window.addEventListener("popstate", async (event) => {
        if (suppressPop) return;
        const url = event.state?.u || new URLSearchParams(location.search).get("u") || "";
        if (!url) return goHome({ replace: false });
        await openAddress(url, { addHistory: false, urlMode: "replace" });
});

/* ── Init ── */
initDefaultProfile();

const hasImport = importProfileFromUrl();
const hasServerImport = (() => {
        const shareToken = new URLSearchParams(location.search).get("share");
        if (!shareToken) return false;
        importServerShare(shareToken);
        return true;
})();

renderFavorites();
applySettings();
updateControls();
browserBar.classList.add("expanded");
renderProfiles();

if (!hasImport && !hasServerImport) {
        const initialUrl = new URLSearchParams(location.search).get("u");
        if (initialUrl) {
                suppressPop = true;
                window.history.replaceState({ u: initialUrl }, "", appShareUrl(initialUrl));
                suppressPop = false;
                openAddress(initialUrl, { addHistory: true, urlMode: "replace" });
        } else {
                window.history.replaceState({ u: "" }, "", location.origin + location.pathname);
        }
}
