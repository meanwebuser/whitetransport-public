import { createServer } from "node:http";
import crypto from "node:crypto";
import { fileURLToPath } from "url";
import path from "node:path";
import { hostname } from "node:os";
import { server as wisp, logging } from "@mercuryworkshop/wisp-js/server";
import Fastify from "fastify";
import fastifyStatic from "@fastify/static";
import { createRemoteBrowserServer } from "./remote-browser.js";

import { scramjetPath } from "@mercuryworkshop/scramjet/path";
import { scramjetPath as scramjetV1Path } from "scramjet-v1/path";
import { baremuxPath } from "@mercuryworkshop/bare-mux/node";

const publicPath = fileURLToPath(new URL("../public/", import.meta.url));
const controllerPath = fileURLToPath(new URL("../public/controller/", import.meta.url));
const scramjetV1ClassicPath = fileURLToPath(new URL("../public/scramjet-v1/", import.meta.url));
const libcurlPath = fileURLToPath(new URL("../node_modules/@mercuryworkshop/libcurl-transport/dist/", import.meta.url));
const epoxyPath = fileURLToPath(new URL("../node_modules/@mercuryworkshop/epoxy-transport/dist/", import.meta.url));

const AUTH_COOKIE = "mobile_browser_auth";
const AUTH_PASSWORD = process.env.MOBILE_PROXY_PASSWORD || "change-me";
const AUTH_SECRET = process.env.MOBILE_PROXY_COOKIE_SECRET || "change-me-secret";
const AUTH_TOKEN = crypto.createHmac("sha256", AUTH_SECRET).update(AUTH_PASSWORD).digest("hex");

function parseCookies(header = "") {
        return Object.fromEntries(header.split(";").map((part) => part.trim()).filter(Boolean).map((part) => {
                const index = part.indexOf("=");
                if (index === -1) return [part, ""];
                return [part.slice(0, index), decodeURIComponent(part.slice(index + 1))];
        }));
}

function isAuthorized(cookieHeader = "") {
        return parseCookies(cookieHeader)[AUTH_COOKIE] === AUTH_TOKEN;
}

function escapeHtml(value = "") {
        return String(value)
                .replaceAll("&", "&amp;")
                .replaceAll("<", "&lt;")
                .replaceAll(">", "&gt;")
                .replaceAll('"', "&quot;")
                .replaceAll("'", "&#39;");
}

function safeNext(value = "/") {
        if (typeof value !== "string" || !value.startsWith("/") || value.startsWith("//")) return "/";
        if (value.startsWith("/login")) return "/";
        return stripPasswordParam(value);
}

function stripPasswordParam(value = "/") {
        const url = new URL(value, "https://local");
        url.searchParams.delete("p");
        const target = `${url.pathname}${url.search}${url.hash}`;
        return target || "/";
}

function setAuthCookie(reply) {
        reply.header("Set-Cookie", `${AUTH_COOKIE}=${AUTH_TOKEN}; Path=/; Max-Age=31536000; HttpOnly; SameSite=Lax; Secure`);
}

function tryPasswordLink(request, reply) {
        const url = new URL(request.url, "https://local");
        const password = url.searchParams.get("p");
        if (password !== AUTH_PASSWORD) return false;
        url.searchParams.delete("p");
        const target = `${url.pathname}${url.search}${url.hash}` || "/";
        setAuthCookie(reply);
        reply.redirect(target);
        return true;
}

function loginPage(error = "", next = "/") {
        const escapedNext = escapeHtml(safeNext(next));
        return `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
<title>Mobile Browser Login</title>
<style>
:root { color-scheme: dark; }
* { box-sizing: border-box; }
body { margin: 0; min-height: 100svh; display: grid; place-items: center; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; background: radial-gradient(circle at top, #243b55, #111 50%); color: #fff; padding: 20px; }
form { width: min(420px, 100%); background: rgba(255,255,255,.08); border: 1px solid rgba(255,255,255,.16); border-radius: 22px; padding: 24px; box-shadow: 0 20px 80px rgba(0,0,0,.35); backdrop-filter: blur(10px); }
h1 { margin: 0 0 8px; font-size: 24px; }
p { opacity: .72; line-height: 1.45; margin: 0 0 18px; }
input, button { width: 100%; height: 52px; border-radius: 15px; font-size: 18px; }
input { border: 1px solid rgba(255,255,255,.22); background: rgba(0,0,0,.28); color: #fff; padding: 0 16px; outline: none; }
button { margin-top: 14px; border: 0; background: #44d07b; color: #06150b; font-weight: 800; }
.err { color: #ff9b9b; min-height: 20px; margin-top: 12px; }
small { display: block; margin-top: 18px; opacity: .55; line-height: 1.45; }
</style>
</head>
<body>
<form method="post" action="/login" autocomplete="current-password">
<h1>Личный браузер</h1>
<p>Пароль сохранится в cookie этого браузера.</p>
<input name="password" type="password" placeholder="Пароль" autofocus required />
<input name="next" type="hidden" value="${escapedNext}" />
<button type="submit">Войти</button>
<div class="err">${escapeHtml(error)}</div>
<small>WhiteTransport remote browser</small>
</form>
</body>
</html>`;
}

logging.set_level(logging.NONE);
const remoteBrowserServer = createRemoteBrowserServer({ isAuthorized });

Object.assign(wisp.options, {
        allow_udp_streams: true,
        hostname_blacklist: [],
        dns_servers: (process.env.WISP_DNS_SERVERS || "1.1.1.1,8.8.8.8").split(",").map((value) => value.trim()).filter(Boolean),
});

const fastify = Fastify({
        logger: false,
        serverFactory: (handler) => {
                return createServer()
                        .on("request", (req, res) => {
                                res.setHeader("Cross-Origin-Opener-Policy", "same-origin");
                                res.setHeader("Cross-Origin-Embedder-Policy", "require-corp");
                                handler(req, res);
                        })
                        .on("upgrade", (req, socket, head) => {
                                const pathname = new URL(req.url || "/", "http://local").pathname;
                                if (pathname === "/remote-browser/ws") {
                                        remoteBrowserServer.handleUpgrade(req, socket, head);
                                } else if (pathname === "/wisp/" && isAuthorized(req.headers.cookie || "")) {
                                        wisp.routeRequest(req, socket, head);
                                } else {
                                        socket.write("HTTP/1.1 401 Unauthorized\r\nConnection: close\r\n\r\n");
                                        socket.destroy();
                                }
                        });
        },
});

fastify.addContentTypeParser("application/x-www-form-urlencoded", { parseAs: "string" }, (request, body, done) => {
        done(null, Object.fromEntries(new URLSearchParams(body)));
});
fastify.addContentTypeParser("application/json", { parseAs: "string" }, (request, body, done) => {
        try { done(null, JSON.parse(body)); }
        catch (err) { done(err); }
});

fastify.get("/healthz", async () => ({ ok: true }));
fastify.get("/login", async (request, reply) => {
        const next = safeNext(request.query?.next || "/");
        if (isAuthorized(request.headers.cookie || "")) return reply.redirect(next);
        return reply.type("text/html").send(loginPage("", next));
});
fastify.post("/login", async (request, reply) => {
        const next = safeNext(request.body?.next || request.query?.next || "/");
        if (request.body?.password === AUTH_PASSWORD) {
                setAuthCookie(reply);
                return reply.redirect(next);
        }
        return reply.code(401).type("text/html").send(loginPage("Неверный пароль", next));
});
fastify.get("/logout", async (request, reply) => {
        reply.header("Set-Cookie", `${AUTH_COOKIE}=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax; Secure`);
        return reply.redirect("/login");
});


/* ── Whitetransport WB API same-origin proxy ── */
const WB_PROXY_ALLOWED_PREFIXES = [
        "https://stream.wb.ru/auth/api/v1/auth/user/guest-register",
        "https://stream.wb.ru/api-room/api/v1/room/",
        "https://stream.wb.ru/api-room-manager/v2/room/",
];

function sanitizeProxyHeaders(headers = {}) {
        const out = {};
        for (const [key, value] of Object.entries(headers || {})) {
                const lower = key.toLowerCase();
                if (["host", "origin", "referer", "cookie", "content-length"].includes(lower)) continue;
                if (typeof value === "string") out[key] = value;
        }
        return out;
}

fastify.post("/api/whitetransport/wb-proxy", async (request, reply) => {
        const { url, method = "GET", headers = {}, body = "" } = request.body || {};
        if (typeof url !== "string" || !WB_PROXY_ALLOWED_PREFIXES.some((prefix) => url.startsWith(prefix))) {
                return reply.code(400).send({ error: "WB proxy URL is not allowed" });
        }
        const upperMethod = String(method || "GET").toUpperCase();
        if (!["GET", "POST"].includes(upperMethod)) {
                return reply.code(405).send({ error: "WB proxy method is not allowed" });
        }
        const upstream = await fetch(url, {
                method: upperMethod,
                headers: sanitizeProxyHeaders(headers),
                body: upperMethod === "GET" ? undefined : body,
        });
        const text = await upstream.text();
        reply.code(upstream.status);
        reply.header("content-type", upstream.headers.get("content-type") || "application/json; charset=utf-8");
        reply.send(text);
});

fastify.addHook("onRequest", async (request, reply) => {
        if (tryPasswordLink(request, reply)) return;
        const pathname = new URL(request.url, "http://local").pathname;
        if (["/healthz", "/login"].includes(pathname)) return;
        if (!isAuthorized(request.headers.cookie || "")) return reply.redirect(`/login?next=${encodeURIComponent(stripPasswordParam(request.url))}`);
});

/* ── Server-side share tokens ── */
const shareTokens = new Map(); // token -> { id, ownerKey, data, views, createdAt, revoked, lastViewAt }

function generateToken() {
        return crypto.randomBytes(6).toString("base64url");
}
function generateOwnerKey() {
        return crypto.randomBytes(12).toString("base64url");
}

/**
 * POST /api/share
 * Body: { profile: { name, favorites, settings, siteData }, ownerKey? }
 * Returns: { token, ownerKey, url, views: 0 }
 */
fastify.post("/api/share", async (request, reply) => {
        const { profile } = request.body || {};
        if (!profile || !profile.name) {
                return reply.code(400).send({ error: "profile.name is required" });
        }
        /* Limit payload size ~2MB */
        const json = JSON.stringify(profile);
        if (json.length > 2 * 1024 * 1024) {
                return reply.code(413).send({ error: "Профиль слишком большой (макс ~2 МБ)" });
        }
        const token = generateToken();
        const ownerKey = request.body?.ownerKey || generateOwnerKey();
        shareTokens.set(token, {
                id: crypto.randomUUID(),
                token,
                ownerKey,
                data: profile,
                views: 0,
                createdAt: Date.now(),
                revoked: false,
                lastViewAt: null,
        });
        const url = `${request.protocol}://${request.host}/?share=${token}`;
        reply.send({ token, ownerKey, url, views: 0 });
});

/**
 * GET /api/share/:token
 * Public: increments views, returns profile data
 * Query ?owner=KEY -> returns full stats (owner mode)
 */
fastify.get("/api/share/:token", async (request, reply) => {
        const { token } = request.params;
        const entry = shareTokens.get(token);
        if (!entry) return reply.code(404).send({ error: "Не найдено" });
        if (entry.revoked) return reply.code(410).send({ error: "Профиль отозван владельцем" });

        const ownerMode = request.query.owner === entry.ownerKey;

        if (!ownerMode) {
                /* Increment view count (once per request) */
                entry.views++;
                entry.lastViewAt = Date.now();
        }

        /* Return limited data for consumers, full for owner */
        if (ownerMode) {
                return reply.send({
                        token: entry.token,
                        name: entry.data.name,
                        favorites: entry.data.favorites,
                        settings: entry.data.settings,
                        siteData: entry.data.siteData,
                        views: entry.views,
                        createdAt: entry.createdAt,
                        lastViewAt: entry.lastViewAt,
                        revoked: entry.revoked,
                        ownerKey: entry.ownerKey,
                });
        }

        return reply.send({
                token: entry.token,
                name: entry.data.name,
                favorites: entry.data.favorites,
                settings: entry.data.settings,
                siteData: entry.data.siteData,
        });
});

/**
 * DELETE /api/share/:token
 * Body: { ownerKey }
 * Revokes the token
 */
fastify.delete("/api/share/:token", async (request, reply) => {
        const { token } = request.params;
        const { ownerKey } = request.body || {};
        const entry = shareTokens.get(token);
        if (!entry) return reply.code(404).send({ error: "Не найдено" });
        if (entry.ownerKey !== ownerKey) return reply.code(403).send({ error: "Неверный ключ управления" });
        entry.revoked = true;
        reply.send({ ok: true, message: "Профиль отозван" });
});

/**
 * GET /api/share/:token/stats
 * Query: ?owner=KEY
 * Returns stats for owner
 */
fastify.get("/api/share/:token/stats", async (request, reply) => {
        const { token } = request.params;
        const entry = shareTokens.get(token);
        if (!entry) return reply.code(404).send({ error: "Не найдено" });
        if (request.query.owner !== entry.ownerKey) return reply.code(403).send({ error: "Неверный ключ управления" });
        return reply.send({
                token: entry.token,
                name: entry.data.name,
                views: entry.views,
                createdAt: entry.createdAt,
                lastViewAt: entry.lastViewAt,
                revoked: entry.revoked,
                favoritesCount: (entry.data.favorites || []).length,
                sitesCount: Object.keys(entry.data.siteData || {}).length,
        });
});

/* Cleanup revoked tokens older than 24h every hour */
setInterval(() => {
        const cutoff = Date.now() - 24 * 60 * 60 * 1000;
        for (const [token, entry] of shareTokens) {
                if (entry.revoked && entry.lastViewAt && entry.lastViewAt < cutoff) {
                        shareTokens.delete(token);
                }
        }
}, 60 * 60 * 1000);

fastify.register(fastifyStatic, { root: publicPath, decorateReply: true });
fastify.register(fastifyStatic, { root: scramjetPath, prefix: "/scramjet/", decorateReply: false });
fastify.register(fastifyStatic, { root: scramjetV1Path, prefix: "/scramjet-v1/", decorateReply: false });
fastify.register(fastifyStatic, { root: scramjetV1ClassicPath, prefix: "/scramjet-v1-classic/", decorateReply: false });
fastify.register(fastifyStatic, { root: controllerPath, prefix: "/controller/", decorateReply: false });
fastify.register(fastifyStatic, { root: libcurlPath, prefix: "/libcurl/", decorateReply: false });
fastify.register(fastifyStatic, { root: epoxyPath, prefix: "/epoxy/", decorateReply: false });
fastify.register(fastifyStatic, { root: baremuxPath, prefix: "/baremux/", decorateReply: false });
fastify.setNotFoundHandler((res, reply) => reply.code(404).type("text/html").sendFile("404.html"));

fastify.server.on("listening", () => {
        const address = fastify.server.address();
        console.log("Listening on:");
        console.log(`\thttp://localhost:${address.port}`);
        console.log(`\thttp://${hostname()}:${address.port}`);
        console.log(`\thttp://${address.family === "IPv6" ? `[${address.address}]` : address.address}:${address.port}`);
});

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
async function shutdown() {
        console.log("SIGTERM signal received: closing HTTP server");
        const forceExit = setTimeout(() => {
                console.warn("Shutdown timeout reached, forcing exit");
                process.exit(0);
        }, 3000);
        forceExit.unref?.();
        await Promise.race([
                (async () => {
                        await remoteBrowserServer.close().catch(() => {});
                        await fastify.close().catch(() => {});
                })(),
                new Promise((resolve) => setTimeout(resolve, 2500)),
        ]);
        clearTimeout(forceExit);
        process.exit(0);
}

let port = parseInt(process.env.PORT || "");
if (isNaN(port)) port = 8098;
const host = process.env.LISTEN_HOST || "127.0.0.1";
fastify.listen({ port, host });
