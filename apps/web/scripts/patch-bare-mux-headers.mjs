#!/usr/bin/env node
import { readFileSync, writeFileSync, existsSync } from 'node:fs';

const patches = [
  {
    files: [
      'node_modules/@mercuryworkshop/bare-mux/dist/index.js',
      'node_modules/@mercuryworkshop/bare-mux/dist/index.mjs',
    ],
    oldText: 'headers:new Headers(t.headers),status:t.status,statusText:t.statusText',
    newText: 'headers:new Headers(t.headers||{}),status:t.status,statusText:t.statusText',
  },
  {
    files: [
      'node_modules/@titaniumnetwork-dev/ultraviolet/dist/uv.bundle.js',
      'public/uv/uv.bundle.js',
    ],
    oldText: 'headers:new Headers(h.headers),status:h.status,statusText:h.statusText',
    newText: 'headers:new Headers(h.headers||{}),status:h.status,statusText:h.statusText',
  },
];
let changed = false;
for (const patch of patches) {
  for (const file of patch.files) {
    if (!existsSync(file)) continue;
    const src = readFileSync(file, 'utf8');
    if (src.includes(patch.newText)) continue;
    if (!src.includes(patch.oldText)) {
      console.warn(`[patch-bare-mux-headers] pattern not found in ${file}`);
      continue;
    }
    writeFileSync(file, src.replace(patch.oldText, patch.newText));
    changed = true;
    console.log(`[patch-bare-mux-headers] patched ${file}`);
  }
}

function patchLibcurlHeaders() {
  const file = 'node_modules/@mercuryworkshop/libcurl-transport/dist/index.mjs';
  if (!existsSync(file)) return false;
  let src = readFileSync(file, 'utf8');
  const requestOld = `  async request(remote, method, body, headers, signal) {
    let headersObj = {};
    for (let [key, value] of headers) {
      headersObj[key] = value;
    }
`;
  const requestNew = `  async request(remote, method, body, headers, signal) {
    let headersObj = {};
    if (headers instanceof Headers || typeof headers?.[Symbol.iterator] === "function") {
      for (let [key, value] of headers) {
        headersObj[key] = value;
      }
    } else if (headers && typeof headers === "object") {
      for (let [key, value] of Object.entries(headers)) {
        if (value == null) continue;
        headersObj[key] = Array.isArray(value) ? value.join(", ") : String(value);
      }
    }
`;
  const connectOld = `  connect(url, protocols, requestHeaders, onopen, onmessage, onclose, onerror) {
    let headersObj = {};
    for (let [key, value] of requestHeaders) {
      headersObj[key] = value;
    }
`;
  const connectNew = `  connect(url, protocols, requestHeaders, onopen, onmessage, onclose, onerror) {
    let headersObj = {};
    if (requestHeaders instanceof Headers || typeof requestHeaders?.[Symbol.iterator] === "function") {
      for (let [key, value] of requestHeaders) {
        headersObj[key] = value;
      }
    } else if (requestHeaders && typeof requestHeaders === "object") {
      for (let [key, value] of Object.entries(requestHeaders)) {
        if (value == null) continue;
        headersObj[key] = Array.isArray(value) ? value.join(", ") : String(value);
      }
    }
`;
  let changedLocal = false;
  if (src.includes(requestOld)) { src = src.replace(requestOld, requestNew); changedLocal = true; }
  if (src.includes(connectOld)) { src = src.replace(connectOld, connectNew); changedLocal = true; }
  if (changedLocal) {
    writeFileSync(file, src);
    console.log(`[patch-bare-mux-headers] patched ${file}`);
  }
  return changedLocal;
}

changed = patchLibcurlHeaders() || changed;


function patchUvRawHeaders() {
  const files = [
    'node_modules/@titaniumnetwork-dev/ultraviolet/dist/uv.sw.js',
    'public/uv/uv.sw.js',
  ];
  const oldText = 'this.headers={};for(let t in s.rawHeaders)this.headers[t.toLowerCase()]=s.rawHeaders[t];';
  const newText = 'this.headers={};let H=s.rawHeaders||{};if(Array.isArray(H)){for(let t of H)Array.isArray(t)&&t.length>=2&&(this.headers[String(t[0]).toLowerCase()]=t[1])}else if(H instanceof Headers){for(let[t,r]of H.entries())this.headers[t.toLowerCase()]=r}else for(let t in H)this.headers[t.toLowerCase()]=H[t];';
  let changedLocal = false;
  for (const file of files) {
    if (!existsSync(file)) continue;
    const src = readFileSync(file, 'utf8');
    if (src.includes(newText)) continue;
    if (!src.includes(oldText)) {
      console.warn(`[patch-bare-mux-headers] rawHeaders pattern not found in ${file}`);
      continue;
    }
    writeFileSync(file, src.replace(oldText, newText));
    changedLocal = true;
    console.log(`[patch-bare-mux-headers] patched ${file}`);
  }
  return changedLocal;
}

changed = patchUvRawHeaders() || changed;


function patchUvSharedWorker() {
  const files = [
    'node_modules/@titaniumnetwork-dev/ultraviolet/dist/uv.handler.js',
    'public/uv/uv.handler.js',
  ];
  const marker = 'PatchedSharedWorker';
  const patch = `
;(()=>{try{const NativeSharedWorker=self.SharedWorker;if(!NativeSharedWorker||NativeSharedWorker.__uvPatched)return;const PatchedSharedWorker=function(url,options){try{const uv=self.__uv;if(uv&&uv.rewriteUrl){url=uv.rewriteUrl(String(url));}}catch{}return new NativeSharedWorker(url,options)};PatchedSharedWorker.prototype=NativeSharedWorker.prototype;Object.defineProperty(PatchedSharedWorker,"__uvPatched",{value:true});self.SharedWorker=PatchedSharedWorker;}catch(err){console.warn("UV SharedWorker patch failed",err)}})();
`;
  let changedLocal = false;
  for (const file of files) {
    if (!existsSync(file)) continue;
    let src = readFileSync(file, 'utf8');
    if (src.includes(marker)) continue;
    src = src.replace('\n//# sourceMappingURL=uv.handler.js.map', patch + '\n//# sourceMappingURL=uv.handler.js.map');
    writeFileSync(file, src);
    changedLocal = true;
    console.log(`[patch-bare-mux-headers] patched ${file}`);
  }
  return changedLocal;
}

changed = patchUvSharedWorker() || changed;

if (!changed) console.log('[patch-bare-mux-headers] no changes needed');
