import assert from 'node:assert/strict';
import { readFile, mkdir, mkdtemp, writeFile, rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import path from 'node:path';
import vm from 'node:vm';
import { spawnSync } from 'node:child_process';
import test from 'node:test';

const extension = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const repo = path.resolve(extension, '../../..');
const formatter = await readFile(path.join(extension, 'lib/export-format.js'), 'utf8');
const worker = await readFile(path.join(extension, 'service-worker.js'), 'utf8');
const id = 'abcdefghijklmnopabcdefghijklmnop';
const sourceURL = 'https://telemost.yandex.ru/j/fixture-room?private=omit#omit';
const cookie = { name: 'Session_id', value: 'synthetic-session', domain: '.yandex.ru', path: '/' };

function harness({
  cookies = [cookie],
  pageCookies = [],
  platform = { os: 'mac', arch: 'arm64' },
  initialStorage = {},
  tabURL = sourceURL,
  dionScopedOnly = false,
  command = { command: 'update' },
} = {}) {
  let listener;
  let installedListener;
  let startupListener;
  let reads = 0;
  let delivered;
  let alarmListener;
  let alarm;
  const storageState = { ...initialStorage };
  let opened = 0;
  const context = vm.createContext({ URL, Date, atob, setTimeout, clearTimeout, self: {}, importScripts() {},
    chrome: {
      runtime: {
        id,
        getPlatformInfo: async () => platform,
        onInstalled: { addListener(fn) { installedListener = fn; } },
        onStartup: { addListener(fn) { startupListener = fn; } },
        onMessage: { addListener(fn) { listener = fn; } },
      },
      tabs: {
        query: async () => [{ id: 7, url: tabURL, active: true, status: 'complete' }],
        get: async () => ({ id: 7, url: tabURL, status: 'complete' }),
        create: async ({ url }) => { opened++; return { id: 8, url, status: 'complete' }; },
        update: async (id, update) => ({ id, url: update.url, status: 'complete' }),
        onUpdated: { addListener() {}, removeListener() {} },
        sendMessage: async () => ({ ok: true, sourceURL: tabURL, cookies: pageCookies }),
      },
      cookies: {
        getAll: async ({ name }) => { reads++; return cookies.filter(c => c.name === name); },
        get: async ({ name }) => {
          reads++; return dionScopedOnly ? undefined : cookies.find(c => c.name === name);
        },
        getAllCookieStores: async () => [{id: 'regular', tabIds: [7]}],
      },
      alarms: {
        onAlarm: { addListener(fn) { alarmListener = fn; } },
        create: async (name, info) => { alarm = { name, ...info }; },
        clear: async name => { if (alarm?.name === name) alarm = undefined; return true; },
      },
      storage: { local: {
        get: async defaults => ({ ...defaults, ...storageState }),
        set: async values => Object.assign(storageState, values),
      } },
      downloads: {}
    },
    WTBrowserBridge: {
      sendBrowserExport: async data => { delivered = data; return { platforms: ['telemost'], count: 1 }; },
      pollRefreshCommand: async () => command,
    }
  });
  vm.runInContext(formatter, context);
  context.WTBrowserExport = context.self.WTBrowserExport;
  vm.runInContext(worker, context);
  return {
    context,
    get reads() { return reads; },
    get delivered() { return delivered; },
    get alarm() { return alarm; },
    fireAlarm() { return alarmListener({ name: 'wt-yandex-telemost-refresh' }); },
    install(reason = 'update') { return installedListener({ reason }); },
    startup() { return startupListener(); },
    get opened() { return opened; },
    async settle() {
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    },
    request(selectionOrMessage = { cookies: true }, sender = { id, url: 'chrome-extension://' + id + '/popup.html' }) {
      const message = selectionOrMessage.type
        ? selectionOrMessage
        : { type: 'wt-send-yandex-telemost', selection: selectionOrMessage };
      return new Promise(resolve => listener(message, sender, resolve));
    }
  };
}

test('the worker starts local polling without a manual pairing', async () => {
  const h = harness();
  await h.install();
  assert.equal(h.alarm.periodInMinutes, 0.5);
  await h.fireAlarm();
  assert.equal(h.delivered.cookies.length, 1);
});

test('polling rejects other sites before opening a tab or reading cookies', async () => {
  const h = harness({ command: { command: 'update', url: 'https://dion.vc.invalid/' } });
  await h.request({ type: 'wt-set-yandex-telemost-polling', enabled: true });
  await h.fireAlarm();
  assert.equal(h.opened, 0);
  assert.equal(h.reads, 0);
  assert.equal(h.delivered, undefined);
});

test('DION sends only the access and refresh cookies from the current store', async () => {
  const dionCookies = ['vc-access-token', 'vc-refresh-token', 'irrelevant'].map(name => (
    { name, value: 'fixture-' + name, domain: '.dion.vc', path: '/' }
  ));
  const h = harness({ tabURL: 'https://dion.vc/?omit', cookies: dionCookies, dionScopedOnly: true });
  assert.equal((await h.request()).ok, true);
  assert.equal(h.delivered.source.url, 'https://dion.vc/');
  assert.deepEqual(Array.from(h.delivered.cookies, cookie => cookie.name), ['vc-access-token', 'vc-refresh-token']);
});

test('DION distinguishes missing refresh and ignores unrelated account cookies', async () => {
  const makeCookie = (name, sid) => ({
    name, value: "e30." + Buffer.from(JSON.stringify({user_id: 1, sid})).toString("base64url") + ".fixture",
    domain: ".dion.vc",
  });
  const current = makeCookie("vc-access-token", "current");
  const stale = makeCookie("vc-access-token", "older");
  const refresh = makeCookie("vc-refresh-token", "current");
  const h = harness({tabURL: "https://dion.vc/", dionScopedOnly:true, cookies:[stale,current,refresh]});
  assert.equal((await h.request()).ok, true);
  assert.equal(h.delivered.cookies[0].value, current.value);
  const missing = harness({tabURL:"https://dion.vc/", cookies:[current]});
  assert.match((await missing.request()).error, /cookie обновления отсутствует/);
  assert.equal(missing.delivered, undefined);
});

test('tracking cookies alone do not count as an authenticated Telemost session', () => {
  const h = harness();
  assert.throws(() => h.context.WTBrowserExport.buildBrowserExport({
    sourceURL, cookies: [{ ...cookie, name: 'yandexuid' }]
  }), /session/i);
  assert.throws(() => h.context.WTBrowserExport.buildBrowserExport({
    sourceURL, cookies: [{ ...cookie, value: '' }]
  }), /session/i);
});

test('worker refuses deselected cookies before reading browser state', async () => {
  const h = harness();
  const result = await h.request({ cookies: false });
  assert.equal(result.ok, false);
  assert.equal(h.reads, 0);
  assert.equal(h.delivered, undefined);
});

test('worker refuses requests from a web page', async () => {
  const h = harness();
  const result = await h.request({ cookies: true }, { id, url: 'https://telemost.yandex.ru/' });
  assert.equal(result.ok, false);
  assert.equal(h.reads, 0);
});

test('worker sends only canonical selected JSON and returns a secret-free summary', async () => {
  const h = harness();
  const result = await h.request();
  assert.equal(result.ok, true);
  assert.equal(result.summary.count, 1);
  assert.equal(h.delivered.source.url, 'https://telemost.yandex.ru/j/fixture-room');
  assert.equal(h.delivered.cookies.length, 1);
  assert.doesNotMatch(JSON.stringify(result), /synthetic-session|fixture-room/);
});

test('worker uses page-visible session cookies when the Chrome cookies API is empty', async () => {
  const h = harness({
    cookies: [],
    pageCookies: [{ name: 'L', value: 'page-session', domain: 'telemost.yandex.ru', path: '/' }],
  });
  const result = await h.request();
  assert.equal(result.ok, true);
  assert.equal(JSON.stringify(h.delivered.cookies.map((value) => value.name)), JSON.stringify(['L']));
  assert.doesNotMatch(JSON.stringify(result), /page-session/);
});

test('fast polling uses a 30-second alarm and stops cleanly', async () => {
  const h = harness();
  const enabled = await h.request({ type: 'wt-set-yandex-telemost-polling', enabled: true });
  assert.equal(enabled.ok, true);
  assert.equal(h.alarm.periodInMinutes, 0.5);
  await h.fireAlarm();
  assert.equal(h.delivered.cookies.length, 1);

  const disabled = await h.request({ type: 'wt-set-yandex-telemost-polling', enabled: false });
  assert.equal(disabled.ok, true);
  assert.equal(h.alarm, undefined);
});

test('a Telemost page wakes headless polling without the popup', async () => {
  const h = harness();
  await h.request({ type: 'wt-set-yandex-telemost-polling', enabled: true });
  const result = await h.request(
    { type: 'wt-browser-page-ready' },
    { id, url: 'https://telemost.yandex.ru/chats', tab: { id: 7 } },
  );
  assert.equal(result.ok, true);
  await new Promise((resolve) => setTimeout(resolve, 0));
  assert.equal(h.delivered.cookies.length, 1);
});

test('saved JSON command works outside the checkout on the prepared Mac', async () => {
  await mkdir(path.join(repo, '.tmp'), { recursive: true });
  const root = await mkdtemp(path.join(repo, '.tmp/extension-command-'));
  try {
    const home = path.join(root, 'home with spaces');
    const downloads = path.join(home, 'Downloads');
    await mkdir(downloads, { recursive: true });
    const input = path.join(root, "session's export.json");
    const payload = JSON.stringify({ fixture: 'synthetic-command-proof' });
    await writeFile(input, payload);
    await writeFile(path.join(downloads, 'wt-browser-credential-bridge-macos-arm64'),
      '#!/bin/sh\n[ "$1" = "--stdin" ] || exit 17\ncat\n', { mode: 0o700 });
    const command = await harness().context.importCommand(input);
    const result = spawnSync('bash', ['-c', command], { cwd: root, env: { ...process.env, HOME: home }, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout, payload);
    assert.doesNotMatch(command, /synthetic-command-proof/);
  } finally { await rm(root, { recursive: true, force: true }); }
});
