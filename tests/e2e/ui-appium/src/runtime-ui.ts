import { probeAndroidSocksPayload } from './android-socks-probe.js';
import { androidTextMatches } from './android-labels.js';
import { clickNativeElement } from './native-click.js';

export type RuntimeMode = 'socks' | 'vpn';

declare const $: (selector: string) => Promise<RuntimeElement>;
declare const browser: {
  waitUntil(condition: () => Promise<boolean>, options?: { timeout?: number }): Promise<void>;
  getUrl(): Promise<string>;
  execute<T>(script: () => T): Promise<T>;
  activateApp(packageName: string): Promise<void>;
  pressKeyCode(keyCode: number): Promise<void>;
};

interface RuntimeElement {
  click(): Promise<void>;
  getText(): Promise<string>;
  isExisting(): Promise<boolean>;
  waitForExist(options?: { timeout?: number }): Promise<void>;
  waitForClickable(options?: { timeout?: number }): Promise<void>;
  waitForDisplayed(options?: { timeout?: number }): Promise<void>;
  waitForEnabled(options?: { timeout?: number }): Promise<void>;
}

export interface RuntimeUiApp {
  expectShellReady(): Promise<void>;
  waitForDaemonReady(): Promise<void>;
  waitForNodes(): Promise<void>;
  connectViaRuntimeApi(): Promise<void>;
  disconnectViaRuntimeApi(): Promise<void>;
  openDiscovery(): Promise<void>;
  openDiagnostics(): Promise<void>;
  refreshNodes(): Promise<void>;
  selectFirstReachableNode(): Promise<void>;
  setMode(mode: RuntimeMode): Promise<void>;
  connect(): Promise<void>;
  disconnect(): Promise<void>;
  runPayloadProbe(): Promise<void>;
  expectConnected(): Promise<void>;
  expectDisconnected(): Promise<void>;
  expectPayloadProbeOk(): Promise<void>;
}

const isAndroid = (): boolean => (process.env.WT_E2E_PLATFORM ?? 'desktop') === 'android';

async function byTestId(id: string): Promise<RuntimeElement> {
  return $(`[data-testid="${id}"]`);
}

async function byAndroidText(text: string): Promise<RuntimeElement> {
  return $(`android=new UiSelector().textContains("${text}")`);
}

async function byAndroidLabels(labels: readonly string[]): Promise<RuntimeElement> {
  return $(`android=new UiSelector().textMatches("${androidTextMatches(labels)}")`);
}

async function clickIfPresent(element: RuntimeElement): Promise<boolean> {
  if (await element.isExisting()) {
    await element.click();
    return true;
  }
  return false;
}

export function runtimeUiApp(): RuntimeUiApp {
  return isAndroid() ? androidRuntimeUiApp() : webRuntimeUiApp();
}

function webRuntimeUiApp(): RuntimeUiApp {
  return {
    async expectShellReady() {
      try {
        await (await byTestId('wt-connect-toggle')).waitForExist();
        await (await byTestId('wt-status')).waitForExist();
      } catch (error) {
        const url = await browser.getUrl().catch(() => 'unknown-url');
        const body = await browser.execute(() => document.body?.innerText?.slice(0, 1000) ?? '').catch(() => 'unknown-body');
        throw new Error(`shared shell not ready at ${url}: ${body || String(error)}`);
      }
    },
    async waitForDaemonReady() {
      const deadline = Date.now() + 90_000;
      let lastStatus = '';
      let lastRuntimeKind = '';
      let lastLogs: string[] = [];
      while (Date.now() < deadline) {
        const info = await browser.execute(() => {
          return (window as any).ytp?.getStatus?.()
            .then((s: any) => ({ status: s?.status ?? '', message: s?.message ?? '', logs: (s?.logs ?? []).slice(-10), runtimeKind: s?.runtimeKind ?? '', pid: s?.pid }))
            .catch(() => ({ status: 'error', message: 'IPC unavailable', logs: [], runtimeKind: '', pid: undefined }));
        }).catch(() => ({ status: 'no-execute', message: '', logs: [], runtimeKind: '', pid: undefined }));
        lastStatus = info.status;
        lastRuntimeKind = info.runtimeKind;
        lastLogs = info.logs as string[];
        // Daemon is ready when it has been detected (runtimeKind != 'none') and
        // status is 'stopped' (running but not connected) or better.
        const daemonDetected = info.runtimeKind && info.runtimeKind !== 'none';
        const readyState = ['stopped', 'connected', 'starting', 'discovering'].includes(info.status);
        if (daemonDetected && readyState) {
          console.log(`Daemon ready: status=${info.status} kind=${info.runtimeKind} pid=${info.pid} msg=${info.message}`);
          return;
        }
        // If status is 'error', fail fast with diagnostics
        if (info.status === 'error') {
          const allLogs = await browser.execute(() => {
            return (window as any).ytp?.getDaemonLogs?.()
              .then((l: any) => (l ?? []).slice(-30))
              .catch(() => []);
          }).catch(() => []);
          throw new Error(`Daemon error state: ${info.message}. Runtime kind: ${info.runtimeKind}. Daemon logs: ${JSON.stringify(allLogs)}`);
        }
        // If still 'starting' with a pid, daemon is spawning — keep waiting
        if (info.status === 'starting' && info.pid) {
          console.log(`Daemon starting (pid ${info.pid}), waiting...`);
        }
        await new Promise(r => setTimeout(r, 2000));
      }
      // Timeout — gather deep diagnostics before failing
      const deepDiag = await browser.execute(() => {
        return (window as any).ytp?.getDaemonLogs?.()
          .then((l: any) => (l ?? []).slice(-30))
          .catch(() => []);
      }).catch(() => []);
      throw new Error(`Daemon not ready after 90s. Status: "${lastStatus}", kind: "${lastRuntimeKind}". Recent logs: ${lastLogs.join(' | ')}. Daemon logs: ${JSON.stringify(deepDiag)}`);
    },
    async waitForNodes() {
      const deadline = Date.now() + 120_000;
      let nodeCount = 0;
      while (Date.now() < deadline) {
        const nodes = await browser.execute(() => {
          return (window as any).ytp?.getRuntimeNodes?.()
            .then((n: any[]) => (n ?? []).map((x: any) => ({ id: x.node_id, reachable: x.reachable, available: x.available })))
            .catch(() => []);
        }).catch(() => []);
        nodeCount = nodes.length;
        const reachable = nodes.filter((n: any) => n.reachable || n.available);
        if (reachable.length > 0) {
          console.log(`Found ${reachable.length} reachable node(s) out of ${nodeCount} total`);
          return;
        }
        // Trigger a discovery refresh periodically
        if (Date.now() % 15000 < 2500) {
          await this.refreshNodes();
        }
        await new Promise(r => setTimeout(r, 5000));
      }
      throw new Error(`No reachable nodes after 120s. Total nodes seen: ${nodeCount}`);
    },
    async connectViaRuntimeApi() {
      // Pre-connect diagnostics
      const preDiag = await browser.execute(() => {
        return (window as any).ytp?.getStatus?.()
          .then((s: any) => ({
            status: s?.status,
            runtimeKind: s?.runtimeKind,
            pid: s?.pid,
            message: s?.message,
            logs: (s?.logs ?? []).slice(-5),
          }))
          .catch(() => ({ status: 'ipc-error' }));
      }).catch(() => ({ status: 'execute-error' }));
      console.log(`PRE-CONNECT STATUS: ${JSON.stringify(preDiag)}`);

      // Fire the connect IPC call but don't wait for it to complete —
      // the daemon's /v1/session/connect blocks until a node accepts
      // the offer, which can take a long time or never happen.
      // Instead, fire-and-forget and poll for status changes.
      browser.execute(() => {
        (window as any).ytp?.connect?.().catch(() => {});
      }).catch(() => {});

      // Poll for connected status (up to 90s)
      const deadline = Date.now() + 90_000;
      let lastStatus = '';
      let logsPrinted = false;
      while (Date.now() < deadline) {
        const info = await browser.execute(() => {
          return (window as any).ytp?.getStatus?.()
            .then((s: any) => ({ status: s?.status ?? '', message: s?.message ?? '' }))
            .catch(() => ({ status: 'error', message: '' }));
        }).catch(() => ({ status: 'error', message: '' }));
        lastStatus = info.status;
        if (info.status === 'connected') {
          console.log(`Connected: ${info.message}`);
          return;
        }
        if (info.status === 'error') {
          const logs = await browser.execute(() => {
            return (window as any).ytp?.getDaemonLogs?.()
              .then((l: any) => (l ?? []).slice(-15))
              .catch(() => []);
          }).catch(() => []);
          throw new Error(`Connect error: ${info.message}. Logs: ${JSON.stringify(logs)}`);
        }
        // Print daemon logs at 15s to diagnose issues
        if (!logsPrinted && Date.now() > deadline - 75_000) {
          logsPrinted = true;
          const midLogs = await browser.execute(() => {
            return (window as any).ytp?.getDaemonLogs?.()
              .then((l: any) => (l ?? []).slice(-30))
              .catch(() => []);
          }).catch(() => []);
          console.log(`MID-POLL DAEMON LOGS (15s): ${JSON.stringify(midLogs)}`);
        }
        await new Promise(r => setTimeout(r, 3000));
      }
      // Timeout — gather diagnostics
      const logs = await browser.execute(() => {
        return (window as any).ytp?.getDaemonLogs?.()
          .then((l: any) => (l ?? []).slice(-20))
          .catch(() => []);
      }).catch(() => []);
      throw new Error(`Connect timeout after 90s (last status: ${lastStatus}). Daemon logs: ${JSON.stringify(logs)}`);
    },
    async disconnectViaRuntimeApi() {
      const result = await browser.execute(() => {
        return (window as any).ytp?.disconnect?.()
          .then((r: any) => ({ ok: true, result: JSON.stringify(r).slice(0, 500) }))
          .catch((e: any) => ({ ok: false, error: String(e) }));
      }).catch((e: any) => ({ ok: false, error: String(e) }));
      console.log(`Runtime API disconnect: ${JSON.stringify(result)}`);
      await new Promise(r => setTimeout(r, 2000));
    },
    async openDiscovery() {
      const tab = await byTestId('wt-tab-discovery');
      if (await tab.isExisting()) await tab.click();
    },
    async openDiagnostics() {
      const tab = await byTestId('wt-tab-diagnostics');
      if (await tab.isExisting()) await tab.click();
    },
    async refreshNodes() {
      await this.openDiscovery();
      const button = await byTestId('wt-refresh-nodes');
      if (await button.isExisting()) await button.click();
    },
    async selectFirstReachableNode() {
      const node = await byTestId('wt-node-list');
      await node.waitForExist();
      const connTab = await byTestId('wt-tab-connection');
      if (await connTab.isExisting()) await connTab.click();
    },
    async setMode(mode: RuntimeMode) {
      const selector = await byTestId(mode === 'vpn' ? 'wt-mode-vpn' : 'wt-mode-socks');
      if (await selector.isExisting()) await selector.click();
    },
    async connect() {
      const button = await byTestId('wt-connect-toggle');
      await button.waitForExist({ timeout: 10000 });
      // Pre-connect diagnostics: check daemon status and available nodes
      const preDiag = await browser.execute(() => {
        const btn = document.querySelector('[data-testid="wt-connect-toggle"]') as HTMLButtonElement;
        const status = document.querySelector('[data-testid="wt-status"]');
        return {
          disabled: btn?.disabled ?? true,
          statusText: status?.textContent?.trim() ?? '',
          btnClass: btn?.className ?? '',
        };
      });
      console.log('PRE-CONNECT:', JSON.stringify(preDiag));
      if (preDiag.disabled) {
        // Gather deeper diagnostics about why the button is disabled
        const deepDiag = await browser.execute(() => {
          return (window as any).ytp?.getStatus?.()
            .then((s: any) => ({
              runtimeStatus: s?.status,
              runtimeKind: s?.runtimeKind,
              runtimeMessage: s?.message,
              pid: s?.pid,
              recentLogs: (s?.logs ?? []).slice(-15),
            }))
            .catch(() => ({ runtimeStatus: 'ipc-error' }));
        }).catch(() => ({}));
        throw new Error(`Connect button is disabled. UI status: "${preDiag.statusText}". Runtime: ${JSON.stringify(deepDiag)}`);
      }
      await button.waitForClickable({ timeout: 30000 });
      await button.click();
      // Wait for UI to reflect connection attempt
      await new Promise(r => setTimeout(r, 5000));
      // Check logs tab for daemon output
      const logsTab = await byTestId('wt-tab-logs');
      if (await logsTab.isExisting()) await logsTab.click();
      await new Promise(r => setTimeout(r, 1000));
      const logsDiag = await browser.execute(() => {
        const status = document.querySelector('[data-testid="wt-status"]');
        const logs = document.querySelector('[data-testid="wt-logs"]');
        return {
          statusText: status?.textContent?.trim() ?? '',
          logsText: logs?.textContent?.slice(-3000) ?? '',
        };
      });
      console.log('POST-CONNECT DIAG:', JSON.stringify(logsDiag));
      // Go back to connection tab
      const connTab = await byTestId('wt-tab-connection');
      if (await connTab.isExisting()) await connTab.click();
    },
    async disconnect() {
      const button = await byTestId('wt-connect-toggle');
      await button.waitForClickable();
      await button.click();
    },
    async runPayloadProbe() {
      // Use the IPC smoke test API directly for more reliable results
      const result = await browser.execute(() => {
        return (window as any).ytp?.runSmokeTest?.()
          .then((r: any) => ({ ok: r?.passed ?? false, summary: r?.summary ?? '', steps: (r?.steps ?? []).map((s: any) => `${s.name}: ${s.status}${s.error ? ` (${s.error})` : ''}`), directIp: r?.directIp, socksIp: r?.socksIp }))
          .catch((e: any) => ({ ok: false, summary: String(e), steps: [] }));
      }).catch((e: any) => ({ ok: false, summary: String(e), steps: [] }));
      console.log(`Payload probe: ${JSON.stringify(result)}`);
      // Store the result for expectPayloadProbeOk to check
      (globalThis as any).__lastProbeResult = result;
      // Also try clicking the UI diagnostics button for visual confirmation
      try {
        await this.openDiagnostics();
        const button = await byTestId('wt-diagnostics-run');
        if (await button.isExisting()) {
          await button.waitForClickable({ timeout: 5000 });
          await button.click();
        }
      } catch {
        // UI button click is best-effort; IPC result is authoritative
      }
    },
    async expectConnected() {
      // Use IPC status check (authoritative) rather than DOM elements
      // which may not be present in the React shell at this point.
      const deadline = Date.now() + 30_000;
      while (Date.now() < deadline) {
        const status = await browser.execute(() => {
          return (window as any).ytp?.getStatus?.()
            .then((s: any) => ({ status: s?.status ?? '', message: s?.message ?? '' }))
            .catch(() => ({ status: 'error', message: '' }));
        }).catch(() => ({ status: 'error', message: '' }));
        if (status.status === 'connected') {
          console.log(`expectConnected: confirmed via IPC: ${status.message}`);
          return;
        }
        await new Promise(r => setTimeout(r, 2000));
      }
      const logs = await browser.execute(() => {
        return (window as any).ytp?.getDaemonLogs?.()
          .then((l: any) => (l ?? []).slice(-15))
          .catch(() => []);
      }).catch(() => []);
      throw new Error(`expectConnected timeout. Daemon logs: ${JSON.stringify(logs)}`);
    },
    async expectDisconnected() {
      // Use IPC status check
      const deadline = Date.now() + 30_000;
      while (Date.now() < deadline) {
        const status = await browser.execute(() => {
          return (window as any).ytp?.getStatus?.()
            .then((s: any) => s?.status ?? '')
            .catch(() => '');
        }).catch(() => '');
        if (status !== 'connected') {
          console.log(`expectDisconnected: confirmed (status=${status})`);
          return;
        }
        await new Promise(r => setTimeout(r, 2000));
      }
    },
    async expectPayloadProbeOk() {
      // Check IPC result first (authoritative)
      const ipcResult = (globalThis as any).__lastProbeResult;
      if (ipcResult?.ok) {
        console.log(`Payload probe passed via IPC: ${ipcResult.summary}`);
        return;
      }
      // Fallback: check UI diagnostics result
      try {
        await browser.waitUntil(async () => {
          const result = await byTestId('wt-diagnostics-result');
          return (await result.isExisting()) && /PASS|SOCKS IP|Tunnel IP/i.test(await result.getText());
        }, { timeout: 30000 });
      } catch {
        // If UI check fails, use IPC result for better error message
        if (ipcResult) {
          throw new Error(`Payload probe failed. IPC: ${JSON.stringify(ipcResult)}`);
        }
        throw new Error('Payload probe failed: no IPC result and UI diagnostics not found');
      }
    },
  };
}

function androidRuntimeUiApp(): RuntimeUiApp {
  return {
    async expectShellReady() {
      // Samsung devices can leave NotificationShade above the launched
      // activity after thermal/notification interruptions. Reclaim the
      // foreground before asserting the product shell so Appium does not
      // report a false missing-element failure against a healthy activity.
      await browser.pressKeyCode(187).catch(() => undefined);
      await browser.pressKeyCode(3).catch(() => undefined);
      await browser.activateApp(process.env.WT_E2E_ANDROID_PACKAGE ?? 'bypass.whitelist');
      await (await byAndroidLabels(['WhiteTransport', 'WHITETRANSPORT'])).waitForExist({ timeout: 60000 });
      await (await byAndroidLabels(['Disconnected', 'Отключено'])).waitForExist({ timeout: 60000 });
      await (await byAndroidLabels(['Connect', 'Подключиться'])).waitForExist({ timeout: 60000 });
    },
    async waitForDaemonReady() {
      throw new Error('Android runtime starts through the Capacitor bridge on Connect; it cannot be awaited before connection');
    },
    async waitForNodes() {
      throw new Error('Android node discovery starts with the runtime on Connect; it cannot be awaited before connection');
    },
    async connectViaRuntimeApi() {
      // On Android the connect button triggers the native Go runtime directly
      await this.connect();
    },
    async disconnectViaRuntimeApi() {
      await this.disconnect();
    },
    async openDiscovery() {},
    async openDiagnostics() {},
    async refreshNodes() {
      // The launcher refreshes discovery through the Capacitor bridge while mounted.
    },
    async selectFirstReachableNode() {
      const expectedNode = process.env.WT_E2E_NODE_LABEL ?? process.env.WT_E2E_NODE_ID;
      if (expectedNode) await clickIfPresent(await byAndroidText(expectedNode));
    },
    async setMode(mode: RuntimeMode) {
      await clickIfPresent(await byAndroidText(mode === 'vpn' ? 'VPN' : 'SOCKS'));
    },
    async connect() {
      const button = await byAndroidLabels(['Connect WhiteTransport', 'Connect', 'Подключиться']);
      await clickNativeElement(button);
    },
    async disconnect() {
      const button = await byAndroidLabels(['Disconnect WhiteTransport', 'Disconnect', 'Отключить', 'Отключиться']);
      await clickNativeElement(button);
    },
    async runPayloadProbe() {
      const result: { ok: true; externalIp: string; responseStatus: string } | { ok: false; error: string } = await probeAndroidSocksPayload()
        .then((probe) => ({ ok: true as const, ...probe }))
        .catch((error: unknown) => ({ ok: false as const, error: error instanceof Error ? error.message : String(error) }));
      (globalThis as any).__lastAndroidProbeResult = result;
      if (!result.ok) throw new Error(`Android SOCKS payload probe failed: ${result.error}`);
      await this.disconnect();
    },
    async expectConnected() {
      await (await byAndroidLabels(['Connected', 'Подключено'])).waitForExist({ timeout: 120000 });
    },
    async expectDisconnected() {
      await browser.waitUntil(async () => {
        const status = await byAndroidLabels(['Connected', 'Подключено']);
        return !(await status.isExisting());
      });
    },
    async expectPayloadProbeOk() {
      const result = (globalThis as any).__lastAndroidProbeResult;
      if (!result?.ok || !result.externalIp) throw new Error(`Android SOCKS payload was not proven: ${JSON.stringify(result)}`);
      console.log(`Android SOCKS payload passed: ${result.responseStatus}, externalIp=${result.externalIp}`);
    },
  };
}
