const MOBILE_BROWSER_SW_VERSION = "202605301243";

self.addEventListener("install", () => {
	self.skipWaiting();
});

self.addEventListener("activate", (event) => {
	event.waitUntil(self.clients.claim());
});

self.addEventListener("message", (event) => {
	if (event.data?.type === "SKIP_WAITING") self.skipWaiting();
});

// Scramjet v2 alpha snapshots URL.createObjectURL during bootstrap.
// ServiceWorkerGlobalScope does not expose it in several browsers, so provide
// a minimal stub before importing the runtime. The proxy path does not need
// object URLs inside the SW; this only prevents bootstrap from crashing.
if (!globalThis.URL.createObjectURL) {
	globalThis.URL.createObjectURL = () => "blob:scramjet-sw-stub";
}
if (!globalThis.URL.revokeObjectURL) {
	globalThis.URL.revokeObjectURL = () => {};
}

importScripts("/scramjet/scramjet.js");
importScripts("/controller/controller.sw.js");
importScripts("/scramjet-v1-classic/scramjet.bundle.classic.js");

let scramjetV1SW = null;
try {
	const { ScramjetServiceWorker } = $scramjetLoadWorker();
	scramjetV1SW = new ScramjetServiceWorker();
} catch (err) {
	console.warn("Scramjet v1 SW bootstrap failed", err);
}

/* ── Cookie injection via SW ── */

/**
 * Cookie store: origin -> [{ name, value, path, domain, secure }]
 * Main page posts { type: "set-cookies", origin, cookies } to inject.
 * SW adds Set-Cookie headers on proxied responses for matching origins.
 */
const cookieStore = new Map();

self.addEventListener("message", (event) => {
	const msg = event.data;
	if (!msg || !msg.type) return;

	if (msg.type === "set-cookies") {
		const { origin, cookies } = msg;
		if (!origin || !Array.isArray(cookies)) return;
		const existing = cookieStore.get(origin) || [];
		for (const c of cookies) {
			const idx = existing.findIndex(e => e.name === c.name);
			if (idx >= 0) existing.splice(idx, 1);
			if (c.value !== null && c.value !== undefined) {
				existing.push(c);
			}
		}
		cookieStore.set(origin, existing);
	}
});

/**
 * Parse Scramjet URL to extract the real target origin.
 * Format: /~/sj/<randomId>/<encoded-real-url>
 */
function getTargetOriginFromRequest(requestUrl) {
	try {
		const pathname = new URL(requestUrl).pathname;
		const match = pathname.match(/^\/~\/sj\/[^/]+\/(.+)$/);
		if (!match) return null;
		const realUrl = decodeURIComponent(match[1]);
		return new URL(realUrl).origin;
	} catch {
		return null;
	}
}

/**
 * Wrap route() to inject Set-Cookie headers into proxied responses.
 * This is the ONLY way to set HttpOnly / __Secure-* cookies in the browser.
 */
const _origRoute = $scramjetController.route.bind($scramjetController);
$scramjetController.route = async function (event) {
	const response = await _origRoute(event);

	const targetOrigin = getTargetOriginFromRequest(event.request.url);
	if (!targetOrigin) return response;

	const stored = cookieStore.get(targetOrigin);
	if (!stored || stored.length === 0) return response;

	// Build Set-Cookie headers from stored cookies
	const setCookieHeaders = stored.map(c => {
		let header = `${c.name}=${c.value}`;
		if (c.path) header += `; Path=${c.path}`;
		else header += "; Path=/";
		if (c.domain) header += `; Domain=${c.domain}`;
		if (c.secure) header += "; Secure";
		if (c.httpOnly) header += "; HttpOnly";
		header += "; Max-Age=31536000";
		header += "; SameSite=Lax";
		return header;
	});

	// Clone response with added Set-Cookie headers
	const newHeaders = new Headers(response.headers);
	for (const sc of setCookieHeaders) {
		newHeaders.append("Set-Cookie", sc);
	}

	return new Response(response.body, {
		status: response.status,
		statusText: response.statusText,
		headers: newHeaders,
	});
};

/* ── Main fetch handler ── */
self.addEventListener("fetch", (event) => {
	try {
		if (scramjetV1SW?.config && scramjetV1SW.route(event)) {
			event.respondWith(scramjetV1SW.fetch(event).catch((err) => {
				console.error("Scramjet v1 fetch failed", err);
				return new Response("Scramjet v1 fetch failed: " + (err?.stack || err?.message || String(err)), {
					status: 502,
					headers: { "content-type": "text/plain; charset=utf-8" },
				});
			}));
			return;
		}
	} catch (err) {
		console.warn("Scramjet v1 route failed", err);
	}
	if ($scramjetController.shouldRoute(event)) {
		event.respondWith($scramjetController.route(event));
	}
});
