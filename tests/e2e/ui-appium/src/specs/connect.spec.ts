import { runtimeUiApp } from '../runtime-ui.js';
import { requiresPreconnectRuntime } from '../runtime-lifecycle.js';

const requireRuntime = process.env.WT_E2E_REQUIRE_RUNTIME === '1';
const platform = process.env.WT_E2E_PLATFORM ?? 'desktop';

describe('WhiteTransport runtime UI', () => {
  it('renders the shared runtime shell', async () => {
    const app = runtimeUiApp();

    await app.expectShellReady();
  });

  it('connects and proves payload through the selected runtime path', async function () {
    if (!requireRuntime) {
      this.skip();
    }

    // Increase timeout for the full payload flow (daemon startup + connect + probe)
    this.timeout(300_000);

    const app = runtimeUiApp();

    if (requiresPreconnectRuntime(platform)) {
      // Desktop owns a daemon that starts before the UI connection action.
      await app.waitForDaemonReady();
      await app.waitForNodes();
    }

    // Connect through the host runtime bridge. On Android this click starts the
    // gomobile runtime, so discovery cannot be a precondition for the action.
    // The Android plugin accepts an omitted serverId and discovers/selects a
    // node while starting the runtime; the pre-connect list may be empty.
    await app.connectViaRuntimeApi();
    await app.expectConnected();

    // Prove payload through the selected runtime. Desktop uses its built-in
    // smoke test; Android forwards the app-owned SOCKS listener over ADB and
    // performs a real SOCKS5 HTTP request before disconnecting through the UI.
    await app.runPayloadProbe();
    await app.expectPayloadProbeOk();

    // Smoke test disconnects internally; verify final state
    await app.expectDisconnected();
  });
});
