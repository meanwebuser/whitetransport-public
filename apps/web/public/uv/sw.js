/*global UVServiceWorker,__uv$config*/

const NativeHeaders = self.Headers;
self.Headers = class SafeHeaders extends NativeHeaders {
    constructor(init) {
        if (init == null) {
            init = {};
        } else if (!(init instanceof NativeHeaders) && typeof init === 'object' && typeof init[Symbol.iterator] !== 'function' && !Array.isArray(init)) {
            const normalized = {};
            for (const [key, value] of Object.entries(init)) {
                if (value == null) continue;
                normalized[key] = Array.isArray(value) ? value.join(', ') : String(value);
            }
            init = normalized;
        }
        super(init);
    }
};
/*
 * Stock service worker script.
 * Users can provide their own sw.js if they need to extend the functionality of the service worker.
 * Ideally, this will be registered under the scope in uv.config.js so it will not need to be modified.
 * However, if a user changes the location of uv.bundle.js/uv.config.js or sw.js is not relative to them, they will need to modify this script locally.
 */
importScripts('uv.bundle.patched.js?v=20260531_sharedworker1');
importScripts('uv.config.js?v=20260531_sharedworker1');
importScripts(__uv$config.sw || 'uv.sw.js');

const uv = new UVServiceWorker();

async function handleRequest(event) {
    if (uv.route(event)) {
        return await uv.fetch(event);
    }
    
    return await fetch(event.request)
}

self.addEventListener('fetch', (event) => {
    event.respondWith(handleRequest(event));
});