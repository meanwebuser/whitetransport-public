import fs from "node:fs/promises";
import path from "node:path";
import { spawn } from "node:child_process";
import net from "node:net";
import WebSocket, { WebSocketServer } from "ws";

const DEFAULT_WIDTH = Number.parseInt(process.env.REMOTE_BROWSER_WIDTH || "390", 10);
const DEFAULT_HEIGHT = Number.parseInt(process.env.REMOTE_BROWSER_HEIGHT || "844", 10);
const DEFAULT_DPR = Number.parseFloat(process.env.REMOTE_BROWSER_DPR || "3") || 3;
const MAX_CAPTURE_PIXELS = Number.parseInt(process.env.REMOTE_BROWSER_MAX_CAPTURE_PIXELS || "3600000", 10);
const SCREENCAST_QUALITY_HIGH = Number.parseInt(process.env.REMOTE_BROWSER_SCREENCAST_QUALITY_HIGH || "62", 10);
const SCREENCAST_QUALITY_LOW = Number.parseInt(process.env.REMOTE_BROWSER_SCREENCAST_QUALITY_LOW || "36", 10);
const SCREENCAST_MAX_BACKLOG = Number.parseInt(process.env.REMOTE_BROWSER_SCREENCAST_MAX_BACKLOG || "2500000", 10);
const SCREENCAST_RECOVER_BACKLOG = Number.parseInt(process.env.REMOTE_BROWSER_SCREENCAST_RECOVER_BACKLOG || "350000", 10);
const SCREENCAST_MIN_RESTART_MS = Number.parseInt(process.env.REMOTE_BROWSER_SCREENCAST_MIN_RESTART_MS || "2000", 10);
const SCREENCAST_EVERY_NTH_HIGH = Number.parseInt(process.env.REMOTE_BROWSER_SCREENCAST_EVERY_NTH_HIGH || "1", 10);
const SCREENCAST_EVERY_NTH_LOW = Number.parseInt(process.env.REMOTE_BROWSER_SCREENCAST_EVERY_NTH_LOW || "2", 10);
const LOW_LATENCY_SCALE = Number.parseFloat(process.env.REMOTE_BROWSER_LOW_LATENCY_SCALE || "0.72") || 0.72;
const REMOTE_BROWSER_PROFILE_DIR = process.env.REMOTE_BROWSER_PROFILE_DIR || path.join(process.cwd(), "data", "remote-chrome-profile");
const REMOTE_BROWSER_PERSISTENT_PROFILE = process.env.REMOTE_BROWSER_PERSISTENT_PROFILE !== "0";
const REMOTE_BROWSER_SINGLE_SESSION = process.env.REMOTE_BROWSER_SINGLE_SESSION !== "0";
const CHROME_CANDIDATES = [
        process.env.REMOTE_BROWSER_CHROME,
        "/usr/bin/google-chrome-stable",
        "/usr/bin/google-chrome",
        "/usr/bin/chromium-browser",
        "/usr/bin/chromium",
].filter(Boolean);

const DEFAULT_PROXY_SERVERS = (process.env.REMOTE_BROWSER_PROXY_SERVERS || process.env.REMOTE_BROWSER_PROXY_SERVER || "")
        .split(",")
        .map((value) => value.trim())
        .filter(Boolean);

function safeUrl(value = "") {
        try {
                const url = new URL(String(value || "about:blank"));
                if (!["http:", "https:", "about:"].includes(url.protocol)) return "about:blank";
                return url.toString();
        } catch {
                return "about:blank";
        }
}

function clampInt(value, min, max, fallback) {
        const number = Math.round(Number(value));
        if (!Number.isFinite(number)) return fallback;
        return Math.max(min, Math.min(max, number));
}

function clampNumber(value, min, max, fallback) {
        const number = Number(value);
        if (!Number.isFinite(number)) return fallback;
        return Math.max(min, Math.min(max, number));
}

function normalizeOrientationType(value, viewportWidth, viewportHeight) {
        const raw = String(value || "").toLowerCase();
        if (raw === "portrait-primary" || raw === "portraitprimary") return "portraitPrimary";
        if (raw === "portrait-secondary" || raw === "portraitsecondary") return "portraitSecondary";
        if (raw === "landscape-primary" || raw === "landscapeprimary") return "landscapePrimary";
        if (raw === "landscape-secondary" || raw === "landscapesecondary") return "landscapeSecondary";
        return viewportHeight >= viewportWidth ? "portraitPrimary" : "landscapePrimary";
}

function sanitizeDeviceProfile(profile = {}) {
        const viewportWidth = clampInt(profile.viewportWidth || profile.width, 240, 1400, DEFAULT_WIDTH);
        const viewportHeight = clampInt(profile.viewportHeight || profile.height, 240, 1800, DEFAULT_HEIGHT);
        let dpr = clampNumber(profile.devicePixelRatio || profile.dpr, 1, 4, DEFAULT_DPR);
        const pixels = viewportWidth * viewportHeight * dpr * dpr;
        if (pixels > MAX_CAPTURE_PIXELS) dpr = Math.max(1, Math.sqrt(MAX_CAPTURE_PIXELS / (viewportWidth * viewportHeight)));
        return {
                viewportWidth,
                viewportHeight,
                devicePixelRatio: Number(dpr.toFixed(3)),
                screenWidth: clampInt(profile.screenWidth, 240, 2000, viewportWidth),
                screenHeight: clampInt(profile.screenHeight, 240, 3000, viewportHeight),
                mobile: profile.mobile !== false,
                touch: profile.touch !== false,
                maxTouchPoints: clampInt(profile.maxTouchPoints, 1, 10, 5),
                userAgent: typeof profile.userAgent === "string" && profile.userAgent.length < 512 ? profile.userAgent : "",
                platform: typeof profile.platform === "string" && profile.platform.length < 80 ? profile.platform : "",
                language: typeof profile.language === "string" && profile.language.length < 32 ? profile.language : "",
                timezone: typeof profile.timezone === "string" && profile.timezone.length < 80 ? profile.timezone : "",
                orientationType: normalizeOrientationType(profile.orientationType, viewportWidth, viewportHeight),
                orientationAngle: clampInt(profile.orientationAngle, -360, 360, 0),
                visualViewportScale: clampNumber(profile.visualViewportScale, 0.1, 8, 1),
        };
}

function decodeDeviceProfile(value) {
        if (!value) return sanitizeDeviceProfile();
        try { return sanitizeDeviceProfile(JSON.parse(Buffer.from(String(value), "base64url").toString("utf8"))); }
        catch { return sanitizeDeviceProfile(); }
}

function proxyToHostPort(proxyServer) {
        try {
                const url = new URL(proxyServer.includes("://") ? proxyServer : `http://${proxyServer}`);
                return { host: url.hostname, port: Number(url.port || (url.protocol === "https:" ? 443 : 80)) };
        } catch {
                return null;
        }
}

async function tcpReachable(host, port, timeoutMs = 1200) {
        return new Promise((resolve) => {
                const socket = net.createConnection({ host, port });
                const timer = setTimeout(() => { socket.destroy(); resolve(false); }, timeoutMs);
                socket.once("connect", () => { clearTimeout(timer); socket.destroy(); resolve(true); });
                socket.once("error", () => { clearTimeout(timer); resolve(false); });
        });
}

async function chooseProxyServer() {
        for (const proxy of DEFAULT_PROXY_SERVERS) {
                const hp = proxyToHostPort(proxy);
                if (!hp) continue;
                if (await tcpReachable(hp.host, hp.port)) return proxy;
        }
        return "";
}

async function fileExists(file) {
        try { await fs.access(file); return true; }
        catch { return false; }
}

async function findChrome() {
        for (const candidate of CHROME_CANDIDATES) {
                if (await fileExists(candidate)) return candidate;
        }
        throw new Error(`Chrome executable not found. Set REMOTE_BROWSER_CHROME. Tried: ${CHROME_CANDIDATES.join(", ")}`);
}

async function freePort() {
        return new Promise((resolve, reject) => {
                const server = net.createServer();
                server.once("error", reject);
                server.listen(0, "127.0.0.1", () => {
                        const port = server.address().port;
                        server.close(() => resolve(port));
                });
        });
}

function sleep(ms) {
        return new Promise((resolve) => setTimeout(resolve, ms));
}

async function waitForJson(port, pathname, timeoutMs = 10000, options = {}) {
        const started = Date.now();
        let lastErr;
        while (Date.now() - started < timeoutMs) {
                try {
                        const res = await fetch(`http://127.0.0.1:${port}${pathname}`, options);
                        if (res.ok) return await res.json();
                        lastErr = new Error(`HTTP ${res.status}`);
                } catch (err) {
                        lastErr = err;
                }
                await sleep(150);
        }
        throw lastErr || new Error(`Timeout waiting for Chrome ${pathname}`);
}

class CdpClient {
        constructor(wsUrl) {
                this.ws = new WebSocket(wsUrl);
                this.nextId = 1;
                this.pending = new Map();
                this.events = new Map();
                this.ws.on("message", (raw) => this.#onMessage(raw));
        }

        async open(timeoutMs = 8000) {
                if (this.ws.readyState === WebSocket.OPEN) return;
                await new Promise((resolve, reject) => {
                        const timer = setTimeout(() => reject(new Error("CDP websocket open timeout")), timeoutMs);
                        this.ws.once("open", () => { clearTimeout(timer); resolve(); });
                        this.ws.once("error", (err) => { clearTimeout(timer); reject(err); });
                });
        }

        #onMessage(raw) {
                let msg;
                try { msg = JSON.parse(raw.toString()); }
                catch { return; }
                if (msg.id && this.pending.has(msg.id)) {
                        const { resolve, reject } = this.pending.get(msg.id);
                        this.pending.delete(msg.id);
                        if (msg.error) reject(new Error(`${msg.error.message || "CDP error"}: ${JSON.stringify(msg.error)}`));
                        else resolve(msg.result || {});
                        return;
                }
                if (msg.method && this.events.has(msg.method)) {
                        for (const fn of this.events.get(msg.method)) {
                                try { fn(msg.params || {}); } catch {}
                        }
                }
        }

        send(method, params = {}) {
                if (this.ws.readyState !== WebSocket.OPEN) return Promise.reject(new Error("CDP websocket is not open"));
                const id = this.nextId++;
                this.ws.send(JSON.stringify({ id, method, params }));
                return new Promise((resolve, reject) => {
                        const timer = setTimeout(() => {
                                this.pending.delete(id);
                                reject(new Error(`CDP ${method} timeout`));
                        }, 15000);
                        this.pending.set(id, {
                                resolve: (value) => { clearTimeout(timer); resolve(value); },
                                reject: (err) => { clearTimeout(timer); reject(err); },
                        });
                });
        }

        on(method, fn) {
                const list = this.events.get(method) || [];
                list.push(fn);
                this.events.set(method, list);
        }

        close() {
                try { this.ws.close(); } catch {}
                for (const { reject } of this.pending.values()) reject(new Error("CDP closed"));
                this.pending.clear();
        }
}

class RemoteBrowserSession {
        constructor(ws, request) {
                this.ws = ws;
                this.request = request;
                this.profile = sanitizeDeviceProfile();
                this.width = this.profile.viewportWidth;
                this.height = this.profile.viewportHeight;
                this.devicePixelRatio = this.profile.devicePixelRatio;
                this.proxyServer = "";
                this.chrome = null;
                this.chromeExitPromise = null;
                this.cdp = null;
                this.userDataDir = null;
                this.persistentProfile = REMOTE_BROWSER_PERSISTENT_PROFILE;
                this.closed = false;
                this.frameId = 0;
                this.currentUrl = "about:blank";
                this.screencast = { active: false, lowLatency: false, lastModeSwitchAt: 0, dropped: 0, sent: 0 };
        }

        send(type, payload = {}) {
                if (this.ws.readyState !== WebSocket.OPEN) return;
                this.ws.send(JSON.stringify({ type, ...payload }));
        }

        log(message, extra = {}) {
                this.send("status", { message, ...extra });
        }

        async start(initialUrl = "about:blank", initialProfile = {}) {
                this.currentUrl = safeUrl(initialUrl);
                await this.updateDeviceProfile(initialProfile, { reconfigure: false });
                const chromePath = await findChrome();
                const port = await freePort();
                this.proxyServer = await chooseProxyServer();
                this.userDataDir = REMOTE_BROWSER_PERSISTENT_PROFILE
                        ? REMOTE_BROWSER_PROFILE_DIR
                        : await fs.mkdtemp(path.join("/tmp", "mobile-browser-remote-"));
                await fs.mkdir(this.userDataDir, { recursive: true, mode: 0o700 });
                const args = [
                        `--remote-debugging-port=${port}`,
                        "--remote-debugging-address=127.0.0.1",
                        `--user-data-dir=${this.userDataDir}`,
                        "--headless=new",
                        "--no-sandbox",
                        "--disable-dev-shm-usage",
                        "--disable-gpu",
                        "--hide-scrollbars",
                        "--autoplay-policy=no-user-gesture-required",
                        `--window-size=${this.width},${this.height}`,
                        "about:blank",
                ];
                if (this.proxyServer) args.splice(args.length - 1, 0, `--proxy-server=${this.proxyServer}`);
                this.chrome = spawn(chromePath, args, {
                        stdio: ["ignore", "ignore", "pipe"],
                        detached: true,
                        env: {
                                ...process.env,
                                HOME: this.userDataDir,
                                XDG_CONFIG_HOME: path.join(this.userDataDir, "config"),
                                XDG_CACHE_HOME: path.join(this.userDataDir, "cache"),
                        },
                });
                this.chromeExitPromise = new Promise((resolve) => {
                        this.chrome.once("exit", (code, signal) => resolve({ code, signal }));
                });
                this.chrome.stderr.on("data", (chunk) => {
                        const text = chunk.toString().trim();
                        if (/Failed to connect to the bus|ALSA lib|alsa_util|PcmOpen|ssl_client_socket_impl|handshake failed/i.test(text)) return;
                        if (/error|failed|fatal/i.test(text)) this.send("log", { level: "warn", text: text.slice(0, 1000) });
                });
                this.chrome.on("exit", (code, signal) => {
                        if (!this.closed) this.send("status", { message: "server browser exited", code, signal });
                });

                await waitForJson(port, "/json/version", 12000);
                const target = await waitForJson(port, `/json/new?${encodeURIComponent(this.currentUrl)}`, 8000, { method: "PUT" });
                this.cdp = new CdpClient(target.webSocketDebuggerUrl);
                await this.cdp.open();
                await this.configurePage();
                await this.cdp.send("Page.bringToFront").catch(() => {});
                this.cdp.on("Page.frameNavigated", (params) => {
                        if (params.frame?.url && params.frame.parentId == null) {
                                this.currentUrl = params.frame.url;
                                this.send("url", { url: this.currentUrl });
                        }
                });
                this.cdp.on("Page.screencastFrame", (params) => this.handleScreencastFrame(params));
                this.log("remote browser ready", { width: this.width, height: this.height, dpr: this.devicePixelRatio, proxy: this.proxyServer || "direct", stream: "cdp-screencast", profile: this.persistentProfile ? "persistent" : "temporary" });
                await this.startScreencast({ lowLatency: false });
        }

        async configurePage() {
                await this.cdp.send("Page.enable");
                await this.cdp.send("Runtime.enable");
                await this.cdp.send("Input.setIgnoreInputEvents", { ignore: false }).catch(() => {});
                await this.cdp.send("Emulation.setDeviceMetricsOverride", {
                        width: this.width,
                        height: this.height,
                        deviceScaleFactor: this.devicePixelRatio,
                        mobile: this.profile.mobile,
                        screenWidth: this.profile.screenWidth,
                        screenHeight: this.profile.screenHeight,
                        positionX: 0,
                        positionY: 0,
                        screenOrientation: { type: this.profile.orientationType, angle: this.profile.orientationAngle },
                });
                if (this.profile.userAgent) {
                        await this.cdp.send("Emulation.setUserAgentOverride", {
                                userAgent: this.profile.userAgent,
                                platform: this.profile.platform || undefined,
                                acceptLanguage: this.profile.language || undefined,
                        }).catch(() => {});
                }
                if (this.profile.timezone) await this.cdp.send("Emulation.setTimezoneOverride", { timezoneId: this.profile.timezone }).catch(() => {});
                await this.cdp.send("Emulation.setTouchEmulationEnabled", { enabled: this.profile.touch, maxTouchPoints: this.profile.maxTouchPoints }).catch(() => {});
        }

        async updateDeviceProfile(profile = {}, { reconfigure = true } = {}) {
                this.profile = sanitizeDeviceProfile({ ...this.profile, ...profile });
                this.width = this.profile.viewportWidth;
                this.height = this.profile.viewportHeight;
                this.devicePixelRatio = this.profile.devicePixelRatio;
                if (reconfigure && this.cdp) {
                        await this.configurePage();
                        await this.restartScreencastSoon();
                }
        }

        async navigate(url) {
                this.currentUrl = safeUrl(url);
                this.frameId = 0;
                this.log("navigate", { url: this.currentUrl });
                await this.cdp?.send("Page.navigate", { url: this.currentUrl });
        }

        async resize(width, height) {
                await this.updateDeviceProfile({ viewportWidth: width, viewportHeight: height });
        }

        screencastSize(lowLatency = this.screencast.lowLatency) {
                const physicalWidth = Math.max(1, Math.round(this.width * this.devicePixelRatio));
                const physicalHeight = Math.max(1, Math.round(this.height * this.devicePixelRatio));
                const scale = lowLatency ? LOW_LATENCY_SCALE : 1;
                return {
                        width: Math.max(240, Math.round(physicalWidth * scale)),
                        height: Math.max(240, Math.round(physicalHeight * scale)),
                };
        }

        async startScreencast({ lowLatency = false } = {}) {
                if (!this.cdp || this.closed) return;
                const size = this.screencastSize(lowLatency);
                this.screencast.lowLatency = lowLatency;
                this.screencast.lastModeSwitchAt = Date.now();
                await this.cdp.send("Page.startScreencast", {
                        format: "jpeg",
                        quality: lowLatency ? SCREENCAST_QUALITY_LOW : SCREENCAST_QUALITY_HIGH,
                        maxWidth: size.width,
                        maxHeight: size.height,
                        everyNthFrame: lowLatency ? SCREENCAST_EVERY_NTH_LOW : SCREENCAST_EVERY_NTH_HIGH,
                });
                this.screencast.active = true;
                this.log("screencast started", { mode: lowLatency ? "low-latency" : "quality", quality: lowLatency ? SCREENCAST_QUALITY_LOW : SCREENCAST_QUALITY_HIGH, width: size.width, height: size.height });
                const frameIdAtStart = this.frameId;
                setTimeout(() => {
                        if (!this.closed && this.screencast.active && this.frameId === frameIdAtStart) {
                                this.captureFallbackFrame("screencast-timeout").catch((err) => this.send("log", { level: "warn", text: `fallback frame failed: ${err.message}` }));
                        }
                }, 1800).unref();
        }

        async captureFallbackFrame(reason = "fallback") {
                if (!this.cdp || this.closed) return;
                const size = this.screencastSize(true);
                const shot = await this.cdp.send("Page.captureScreenshot", { format: "jpeg", quality: SCREENCAST_QUALITY_LOW, fromSurface: true, captureBeyondViewport: false });
                if (!shot?.data) return;
                this.frameId += 1;
                this.send("frame", {
                        frameId: this.frameId,
                        mode: "fallback-screenshot",
                        key: true,
                        image: shot.data,
                        width: size.width,
                        height: size.height,
                        viewportWidth: this.width,
                        viewportHeight: this.height,
                        dpr: Number((size.width / this.width).toFixed(3)) || this.devicePixelRatio,
                        backlog: this.ws.bufferedAmount || 0,
                        lowLatency: true,
                        dropped: this.screencast.dropped,
                        reason,
                });
        }

        async stopScreencast() {
                if (!this.cdp || !this.screencast.active) return;
                await this.cdp.send("Page.stopScreencast").catch(() => {});
                this.screencast.active = false;
        }

        async restartScreencastSoon({ lowLatency = this.screencast.lowLatency } = {}) {
                if (!this.cdp || this.closed) return;
                const now = Date.now();
                if (now - this.screencast.lastModeSwitchAt < SCREENCAST_MIN_RESTART_MS) return;
                await this.stopScreencast();
                await this.startScreencast({ lowLatency });
        }

        async maybeAdaptScreencast() {
                const backlog = this.ws.bufferedAmount || 0;
                if (!this.screencast.lowLatency && backlog > SCREENCAST_MAX_BACKLOG) {
                        await this.restartScreencastSoon({ lowLatency: true });
                } else if (this.screencast.lowLatency && backlog < SCREENCAST_RECOVER_BACKLOG) {
                        await this.restartScreencastSoon({ lowLatency: false });
                }
        }

        async handleScreencastFrame(params = {}) {
                if (this.closed || !params.data) return;
                await this.cdp?.send("Page.screencastFrameAck", { sessionId: params.sessionId }).catch(() => {});
                const backlog = this.ws.bufferedAmount || 0;
                if (backlog > SCREENCAST_MAX_BACKLOG * 2) {
                        this.screencast.dropped += 1;
                        await this.maybeAdaptScreencast();
                        return;
                }
                this.frameId += 1;
                this.screencast.sent += 1;
                const cssWidth = Math.max(1, Math.round(params.metadata?.deviceWidth || this.width));
                const cssHeight = Math.max(1, Math.round(params.metadata?.deviceHeight || this.height));
                const size = this.screencastSize();
                this.send("frame", {
                        frameId: this.frameId,
                        mode: "screencast",
                        key: true,
                        image: params.data,
                        width: size.width,
                        height: size.height,
                        viewportWidth: cssWidth,
                        viewportHeight: cssHeight,
                        dpr: Number((size.width / cssWidth).toFixed(3)) || this.devicePixelRatio,
                        backlog,
                        lowLatency: this.screencast.lowLatency,
                        dropped: this.screencast.dropped,
                });
                if (this.frameId % 10 === 0) await this.maybeAdaptScreencast();
        }

        async pointer(input) {
                if (!this.cdp) return;
                const x = Math.max(0, Math.min(this.width, Number(input.x) || 0));
                const y = Math.max(0, Math.min(this.height, Number(input.y) || 0));
                const action = input.action;
                const pointerType = input.pointerType || "touch";
                if (pointerType === "touch") {
                        const type = action === "down" ? "touchStart" : action === "up" || action === "cancel" ? "touchEnd" : "touchMove";
                        const touchPoints = type === "touchEnd" ? [] : [{ x, y, radiusX: 2, radiusY: 2, force: input.pressure || 0.5, id: Number(input.pointerId) || 1 }];
                        await this.cdp.send("Input.dispatchTouchEvent", { type, touchPoints, modifiers: 0 }).catch(() => {});
                        return;
                }
                const type = action === "down" ? "mousePressed" : action === "up" ? "mouseReleased" : "mouseMoved";
                await this.cdp.send("Input.dispatchMouseEvent", { type, x, y, button: "left", buttons: type === "mouseReleased" ? 0 : 1, clickCount: 1 }).catch(() => {});
        }

        async wheel(input) {
                if (!this.cdp) return;
                await this.cdp.send("Input.dispatchMouseEvent", {
                        type: "mouseWheel",
                        x: Number(input.x) || this.width / 2,
                        y: Number(input.y) || this.height / 2,
                        deltaX: Number(input.deltaX) || 0,
                        deltaY: Number(input.deltaY) || 0,
                }).catch(() => {});
        }

        async key(input) {
                if (!this.cdp) return;
                if (typeof input.text === "string" && input.text.length > 0) {
                        await this.cdp.send("Input.insertText", { text: input.text }).catch(() => {});
                        return;
                }
                const key = String(input.key || "");
                if (!key) return;
                const code = String(input.code || key);
                const type = input.action === "up" ? "keyUp" : "keyDown";
                const windowsVirtualKeyCode = key.length === 1 ? key.toUpperCase().charCodeAt(0) : 0;
                await this.cdp.send("Input.dispatchKeyEvent", { type, key, code, windowsVirtualKeyCode }).catch(() => {});
        }

        async handle(raw) {
                let msg;
                try { msg = JSON.parse(raw.toString()); }
                catch { return; }
                try {
                        if (msg.type === "open") await this.navigate(msg.url);
                        else if (msg.type === "device") await this.updateDeviceProfile(msg.profile || msg);
                        else if (msg.type === "resize") await this.resize(msg.width, msg.height);
                        else if (msg.type === "pointer") await this.pointer(msg);
                        else if (msg.type === "wheel") await this.wheel(msg);
                        else if (msg.type === "key") await this.key(msg);
                        else if (msg.type === "text") await this.key({ text: msg.text || "" });
                } catch (err) {
                        this.send("log", { level: "error", text: err.message });
                }
        }

        async close() {
                this.closed = true;
                this.cdp?.send("Page.stopScreencast").catch(() => {});
                this.cdp?.close();
                this.cdp = null;
                if (this.chrome && !this.chrome.killed) {
                        const pid = this.chrome.pid;
                        try { process.kill(-pid, "SIGTERM"); } catch { try { this.chrome.kill("SIGTERM"); } catch {} }
                        const exited = this.chromeExitPromise
                                ? await Promise.race([this.chromeExitPromise, sleep(1200).then(() => null)])
                                : null;
                        if (!exited) {
                                try { process.kill(-pid, "SIGKILL"); } catch { try { this.chrome.kill("SIGKILL"); } catch {} }
                                if (this.chromeExitPromise) await Promise.race([this.chromeExitPromise, sleep(800)]).catch(() => {});
                        }
                }
                if (this.userDataDir && !this.persistentProfile) fs.rm(this.userDataDir, { recursive: true, force: true }).catch(() => {});
        }
}

export function createRemoteBrowserServer({ isAuthorized }) {
        const wss = new WebSocketServer({ noServer: true, perMessageDeflate: false, maxPayload: 32 * 1024 * 1024 });
        const sessions = new Set();

        wss.on("connection", async (ws, request) => {
                if (REMOTE_BROWSER_SINGLE_SESSION) {
                        for (const existing of [...sessions]) {
                                sessions.delete(existing);
                                try { existing.ws.close(1000, "replaced by a new remote browser session"); } catch {}
                                await existing.close().catch(() => {});
                        }
                }
                const url = new URL(request.url, "http://local");
                const session = new RemoteBrowserSession(ws, request);
                sessions.add(session);
                ws.on("message", (raw) => session.handle(raw));
                ws.on("close", () => { sessions.delete(session); session.close(); });
                ws.on("error", () => { sessions.delete(session); session.close(); });
                session.start(url.searchParams.get("u") || "about:blank", decodeDeviceProfile(url.searchParams.get("device"))).catch((err) => {
                        session.send("log", { level: "error", text: err.message });
                        ws.close(1011, "remote browser start failed");
                });
        });

        return {
                handleUpgrade(request, socket, head) {
                        if (!isAuthorized(request.headers.cookie || "")) {
                                socket.write("HTTP/1.1 401 Unauthorized\r\nConnection: close\r\n\r\n");
                                socket.destroy();
                                return;
                        }
                        wss.handleUpgrade(request, socket, head, (ws) => wss.emit("connection", ws, request));
                },
                async close() {
                        for (const session of sessions) await session.close();
                        for (const client of wss.clients) {
                                try { client.terminate(); } catch {}
                        }
                        await new Promise((resolve) => wss.close(resolve));
                },
        };
}
