import { describe, expect, it, vi } from 'vitest';

import {
  createCapacitorBackendWithNodes,
  createLegacyDesktopBackend,
  createWailsBackend,
  EndpointUnavailableError,
  getWailsApp,
  hasWailsHost,
  resolveTransportBackend,
  UnsupportedCapabilityError,
  normalizeNativeStatus,
  type CapacitorWtTransportPlugin,
  type WailsAppBridge,
} from './wt-transport';
import {
  connectionStateFromStatus,
  mapConnectionLifecycle,
  normalizeLocalUserAuthSessionStatus,
  normalizeSystemVPNState,
} from './transport-contract';

const desktopStatus = {
  state: 'connected',
  connected: true,
  transport_state: 'connected',
  system_vpn_state: 'unsupported',
  message: 'Connected via node-1',
  runtime_state: 'connected',
  active_node_id: 'node-1',
  session_id: 'session-1',
  socks_listen: '127.0.0.1:8809',
  discovered_servers: 1,
  available_servers: 1,
  healthy_carriers: 1,
  unhealthy_carriers: 0,
  runtime_build: { version: 'dev', commit: 'abc', date: 'today' },
  diagnostics_available: true,
};

function wailsBridge(): WailsAppBridge {
  return {
    GetStatus: vi.fn().mockResolvedValue(desktopStatus),
    ListServers: vi.fn().mockResolvedValue([
      { id: 'node-1', label: 'Berlin', country: 'DE', region: 'Berlin', available: true, capabilities: ['egress'] },
    ]),
    Connect: vi.fn().mockResolvedValue(desktopStatus),
    Disconnect: vi.fn().mockResolvedValue({ ...desktopStatus, state: 'disconnected', connected: false, transport_state: 'off' }),
    GetTelemetry: vi.fn().mockResolvedValue({ external_ip: '198.51.100.10', latency_ms: 42, active_node_id: 'node-1' }),
    GetLogFilePath: vi.fn().mockResolvedValue('/tmp/whitetransport.log'),
    ReadLogs: vi.fn().mockResolvedValue([{ timestamp: '2026-07-20T07:00:00Z', level: 'info', message: 'connected' }]),
    GetRoutingSettings: vi.fn().mockResolvedValue({ mode: 'all_proxy', lan_access: false }),
    SetRoutingSettings: vi.fn().mockResolvedValue({ mode: 'ru_direct', lan_access: true }),
  };
}

describe('shared native host contract', () => {
  it('exposes only credential-free local user auth status across the Wails bridge', async () => {
    const bridge = {
      ...wailsBridge(),
      GetLocalUserAuthSessionStatus: vi.fn().mockResolvedValue({
        provider: 'VK',
        connected: true,
        expired: false,
        account_label: 'Alice',
        expires_at: '2030-01-01T00:00:00Z',
        access_token: 'must-not-cross-bridge',
        refresh_token: 'must-not-cross-bridge',
        cookies: 'must-not-cross-bridge',
        headers: { authorization: 'must-not-cross-bridge' },
        token_store: { tokens: [{ value: 'must-not-cross-bridge' }] },
      }),
    } satisfies WailsAppBridge;

    const status = await createWailsBackend(bridge).getLocalUserAuthSessionStatus?.();
    expect(status).toEqual({
      provider: 'vk',
      connected: true,
      expired: false,
      accountLabel: 'Alice',
      expiresAt: '2030-01-01T00:00:00Z',
    });
    const serialized = JSON.stringify(status);
    expect(serialized).not.toMatch(/access_token|refresh_token|cookies|headers|token_store|TokenStore/i);
    expect(normalizeLocalUserAuthSessionStatus({ provider: 'ok', state: 'expired', token_store: {} })).toEqual({
      provider: 'ok',
      connected: false,
      expired: true,
    });
  });

  it('preserves the exact macOS lifecycle and system VPN wire states', () => {
    expect(mapConnectionLifecycle('unsupported')).toBe('unsupported');
    expect(normalizeSystemVPNState('disconnecting')).toBe('disconnecting');
    expect(connectionStateFromStatus({
      state: 'disconnecting',
      status: 'disconnecting',
      active: false,
      mode: 'tunnel',
      transportState: 'connected',
      systemVpnState: 'disconnecting',
    })).toMatchObject({
      lifecycle: 'disconnecting',
      transportState: 'connected',
      systemVpnState: 'disconnecting',
    });
  });

  it('detects the Wails desktop bridge by its generated go.main.App surface', () => {
    expect(hasWailsHost({ go: { main: { App: {} } } })).toBe(true);
    expect(hasWailsHost({ go: { main: {} } })).toBe(false);
    expect(hasWailsHost(undefined)).toBe(false);
  });

  it('keeps Wails helpers SSR-safe and resolves a bridge injected after import', async () => {
    expect(getWailsApp(undefined)).toBeNull();

    const root: Record<string, unknown> = {};
    await expect(resolveTransportBackend(root).getCapabilities()).resolves.toMatchObject({ host: 'browser' });
    await expect(resolveTransportBackend(root).getRuntimeNodes()).rejects.toMatchObject({
      code: 'UNSUPPORTED_CAPABILITY',
      capability: 'endpoints',
    });

    root.go = { main: { App: wailsBridge() } };
    await expect(resolveTransportBackend(root).getCapabilities()).resolves.toMatchObject({ host: 'wails' });
  });

  it('uses transport_state before product and daemon state when mapping Wails status', async () => {
    const bridge = wailsBridge();
    bridge.GetStatus = vi.fn().mockResolvedValue({
      ...desktopStatus,
      state: 'connected',
      runtime_state: 'connected',
      transport_state: 'disconnected',
    });

    const backend = createWailsBackend(bridge);
    await expect(backend.getStatus()).resolves.toMatchObject({
      status: 'disconnected',
      transportState: 'disconnected',
      active: false,
    });
    await expect(backend.getConnectionState()).resolves.toMatchObject({ lifecycle: 'disconnected' });
  });

  it('preserves degraded product state in desktop status when transport remains connected', async () => {
    const bridge = wailsBridge();
    bridge.GetStatus = vi.fn().mockResolvedValue({
      ...desktopStatus,
      state: 'degraded',
      transport_state: 'connected',
      runtime_state: 'connected',
    });

    await expect(createWailsBackend(bridge).getDesktopStatus?.()).resolves.toMatchObject({
      status: 'degraded',
      daemonState: 'connected',
    });
  });

  it('merges Wails telemetry into desktop status', async () => {
    const bridge = wailsBridge();
    bridge.GetTelemetry = vi.fn().mockResolvedValue({
      external_ip: '198.51.100.15',
      latency_ms: 57,
      error: 'probe degraded',
    });

    await expect(createWailsBackend(bridge).getDesktopStatus?.()).resolves.toMatchObject({
      externalIp: '198.51.100.15',
      latencyMs: 57,
      error: 'probe degraded',
    });
    expect(bridge.GetTelemetry).toHaveBeenCalledOnce();
  });

  it.each([undefined, '', 'not-an-address', '127.0.0.1:0', '127.0.0.1:not-a-port', '127.0.0.1:123abc'])(
    'rejects unavailable Wails SOCKS endpoint %s',
    async (socksListen) => {
      const bridge = wailsBridge();
      bridge.GetStatus = vi.fn().mockResolvedValue({ ...desktopStatus, socks_listen: socksListen });

      await expect(createWailsBackend(bridge).getSocksInfo()).rejects.toBeInstanceOf(EndpointUnavailableError);
      await expect(createWailsBackend(bridge).getSocksInfo()).rejects.toMatchObject({
        code: 'ENDPOINT_UNAVAILABLE',
        endpoint: 'socks',
      });
    },
  );

  it('maps Wails status, endpoints, logs, and routing into the unified contract', async () => {
    const bridge = wailsBridge();
    const backend = createWailsBackend(bridge);

    await expect(backend.getConnectionState()).resolves.toMatchObject({
      lifecycle: 'degraded',
      transportState: 'connected',
      systemVpnState: 'unsupported',
      selectedNodeId: 'node-1',
    });
    await expect(backend.getStatus()).resolves.toMatchObject({ active: false, transportState: 'connected', systemVpnState: 'unsupported' });
    await expect(backend.getRuntimeNodes()).resolves.toEqual([
      expect.objectContaining({ node_id: 'node-1', label: 'Berlin', available: true, capabilities: ['egress'] }),
    ]);
    await expect(backend.getLogInfo()).resolves.toEqual({
      path: '/tmp/whitetransport.log',
      lines: ['connected'],
      persistent: true,
    });
    await expect(backend.getProxyRouting()).resolves.toEqual({ mode: 'all_proxy', lan_access: false });
    await expect(backend.setProxyRouting({ mode: 'ru_direct', lan_access: true })).resolves.toEqual({ mode: 'ru_direct', lan_access: true });
    await expect(backend.getSplitRouting()).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY', capability: 'split_routing' });
    await expect(backend.setSplitRouting({ mode: 'ru_direct', lan_access: true })).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY', capability: 'split_routing' });
  });

  it('reports Wails capabilities without claiming unsupported system VPN methods', async () => {
    const backend = createWailsBackend(wailsBridge());
    await expect(backend.getCapabilities()).resolves.toMatchObject({
      host: 'wails',
      systemVpn: false,
      requestVpnPermission: false,
      startSystemVpn: false,
      stopSystemVpn: false,
      splitRouting: false,
      proxyRouting: true,
      logs: true,
    });
    await expect(backend.startSystemVPN()).rejects.toBeInstanceOf(UnsupportedCapabilityError);
    await expect(backend.requestVPNPermission()).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY', capability: 'vpn_permission' });
  });

  it('delegates dynamic Wails capabilities, system VPN lifecycle, and destination split routing', async () => {
    const bridge = {
      ...wailsBridge(),
      GetCapabilities: vi.fn().mockResolvedValue({
        host: 'wails', transport: true, endpoints: true, logs: true,
        splitRouting: true, proxyRouting: true, systemVpn: true,
        requestVpnPermission: true, startSystemVpn: true, stopSystemVpn: true,
        smokeTest: true,
      }),
      RequestSystemVPNPermission: vi.fn().mockResolvedValue('permission_required'),
      StartSystemVPN: vi.fn().mockResolvedValue({ state: 'connected' }),
      StopSystemVPN: vi.fn().mockResolvedValue('disconnected'),
      GetSplitRoutingSettings: vi.fn().mockResolvedValue({ mode: 'bypass', lan_access: true, destinations: ['198.51.100.0/24'] }),
      SetSplitRoutingSettings: vi.fn().mockResolvedValue({ mode: 'only', lan_access: false, destinations: ['2001:db8::/32'] }),
      RunTestMode: vi.fn().mockResolvedValue({ passed: true, activeNodeId: 'node-1', externalIp: '203.0.113.10', logPath: '/tmp/wt.log', resultPath: '/tmp/wt-result.json' }),
    } satisfies WailsAppBridge;
    const backend = createWailsBackend(bridge);

    await expect(backend.getCapabilities()).resolves.toEqual({
      host: 'wails', transport: true, endpoints: true, logs: true,
      splitRouting: true, proxyRouting: true, systemVpn: true,
      requestVpnPermission: true, startSystemVpn: true, stopSystemVpn: true,
      smokeTest: true,
    });
    await backend.requestVPNPermission();
    await backend.startSystemVPN();
    await backend.stopSystemVPN();
    await expect(backend.getSplitRouting()).resolves.toEqual({ mode: 'bypass', lan_access: true, destinations: ['198.51.100.0/24'] });
    await expect(backend.setSplitRouting({ mode: 'only', lan_access: false, destinations: ['2001:db8::/32'] })).resolves.toEqual({
      mode: 'only', lan_access: false, destinations: ['2001:db8::/32'],
    });
    await expect(backend.runSmokeTest!({ nodeId: 'node-1' })).resolves.toMatchObject({
      passed: true, selectedNodeId: 'node-1', logPath: '/tmp/wt.log', resultPath: '/tmp/wt-result.json',
    });
    expect(bridge.RequestSystemVPNPermission).toHaveBeenCalledOnce();
    expect(bridge.StartSystemVPN).toHaveBeenCalledOnce();
    expect(bridge.StopSystemVPN).toHaveBeenCalledOnce();
    expect(bridge.SetSplitRoutingSettings).toHaveBeenCalledWith('only', false, ['2001:db8::/32']);
    expect(bridge.RunTestMode!).toHaveBeenCalledWith('node-1');
  });

  it('keeps the Capacitor bridge honest while preserving node discovery', async () => {
    const plugin: CapacitorWtTransportPlugin = {
      getStatus: vi.fn().mockResolvedValue({ status: 'CALL_DISCONNECTED', active: false, mode: 'off' }),
      connect: vi.fn().mockResolvedValue(undefined),
      disconnect: vi.fn().mockResolvedValue(undefined),
      getSocksInfo: vi.fn().mockResolvedValue({ host: '127.0.0.1', port: 1080 }),
      listNodes: vi.fn().mockResolvedValue({ nodes: [{ node_id: 'node-android', available: true }] }),
      addListener: vi.fn().mockResolvedValue(undefined),
    };
    const backend = createCapacitorBackendWithNodes(plugin);

    await expect(backend.getCapabilities()).resolves.toMatchObject({
      host: 'capacitor',
      systemVpn: false,
      requestVpnPermission: false,
      startSystemVpn: false,
      stopSystemVpn: false,
      splitRouting: false,
    });
    await expect(backend.getConnectionState()).resolves.toMatchObject({ lifecycle: 'disconnected', systemVpnState: 'unsupported' });
    await expect(backend.getRuntimeNodes()).resolves.toEqual([{ node_id: 'node-android', available: true }]);
    await expect(backend.getProxyRouting()).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY', capability: 'proxy_routing' });
    await expect(backend.setMode({ mode: 'tunnel' })).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });

    const setMode = vi.fn().mockResolvedValue(undefined);
    const modeBackend = createCapacitorBackendWithNodes({ ...plugin, setMode });
    await modeBackend.setMode({ mode: 'tunnel' });
    expect(setMode).toHaveBeenCalledWith({ mode: 'tunnel' });

    const transportOnlyBackend = createCapacitorBackendWithNodes({
      ...plugin,
      getStatus: vi.fn().mockResolvedValue({ status: 'connected', active: true, mode: 'proxy' }),
    });
    await expect(transportOnlyBackend.getConnectionState()).resolves.toMatchObject({ lifecycle: 'degraded', systemVpnState: 'unsupported' });
  });

  it('reads bounded Capacitor logs when the native plugin advertises support', async () => {
    const plugin: CapacitorWtTransportPlugin = {
      getStatus: vi.fn().mockResolvedValue({ status: 'off', active: false, mode: 'off' }),
      getLogInfo: vi.fn().mockResolvedValue({ path: '/data/cache/relay.log', lines: ['safe'], persistent: true }),
      connect: vi.fn().mockResolvedValue(undefined),
      disconnect: vi.fn().mockResolvedValue(undefined),
      getSocksInfo: vi.fn().mockResolvedValue({ host: '127.0.0.1', port: 1080 }),
      listNodes: vi.fn().mockResolvedValue({ nodes: [] }),
    };
    const backend = createCapacitorBackendWithNodes(plugin);

    await expect(backend.getLogInfo()).resolves.toEqual({
      path: '/data/cache/relay.log',
      lines: ['safe'],
      persistent: true,
    });
    expect(plugin.getLogInfo).toHaveBeenCalledOnce();
  });

  it('delegates the current Capacitor system VPN and split-routing contract', async () => {
    const plugin = {
      getStatus: vi.fn().mockResolvedValue({ status: 'connected', active: true, mode: 'tunnel', transport_state: 'connected', system_vpn_state: 'connected' }),
      getConnectionState: vi.fn().mockResolvedValue({ lifecycle: 'connected', state: 'connected', status: 'connected', active: true, mode: 'tunnel', transport_state: 'connected', system_vpn_state: 'connected' }),
      getCapabilities: vi.fn().mockResolvedValue({
        host: 'capacitor', transport: true, endpoints: true, logs: false, splitRouting: true, proxyRouting: false,
        systemVpn: true, requestVpnPermission: true, startSystemVpn: true, stopSystemVpn: true, smokeTest: false,
      }),
      requestVPNPermission: vi.fn().mockResolvedValue(undefined),
      startSystemVPN: vi.fn().mockResolvedValue(undefined),
      stopSystemVPN: vi.fn().mockResolvedValue(undefined),
      getSplitRouting: vi.fn().mockResolvedValue({ mode: 'bypass', lan_access: false, packages: ['com.example.browser'] }),
      setSplitRouting: vi.fn().mockImplementation(async (settings) => settings),
      connect: vi.fn().mockResolvedValue(undefined),
      disconnect: vi.fn().mockResolvedValue(undefined),
      setMode: vi.fn().mockResolvedValue(undefined),
      getSocksInfo: vi.fn().mockResolvedValue({ host: '127.0.0.1', port: 1080 }),
      listNodes: vi.fn().mockResolvedValue({ nodes: [] }),
      addListener: vi.fn().mockResolvedValue(undefined),
    } as unknown as CapacitorWtTransportPlugin;
    const backend = createCapacitorBackendWithNodes(plugin);

    await expect(backend.getCapabilities()).resolves.toMatchObject({ systemVpn: true, splitRouting: true });
    await expect(backend.getConnectionState()).resolves.toMatchObject({ lifecycle: 'connected', transportState: 'connected', systemVpnState: 'connected' });
    await backend.requestVPNPermission();
    await backend.startSystemVPN();
    await backend.stopSystemVPN();
    await expect(backend.getSplitRouting()).resolves.toEqual({ mode: 'bypass', lan_access: false, packages: ['com.example.browser'] });
    await expect(backend.setSplitRouting({ mode: 'only', lan_access: false, packages: ['com.example.chat'] })).resolves.toEqual({
      mode: 'only', lan_access: false, packages: ['com.example.chat'],
    });
    expect(plugin.requestVPNPermission).toHaveBeenCalledOnce();
    expect(plugin.startSystemVPN).toHaveBeenCalledOnce();
    expect(plugin.stopSystemVPN).toHaveBeenCalledOnce();
  });

  it('maps permission-required system state and validates unknown native values', async () => {
    const bridge = wailsBridge();
    bridge.GetStatus = vi.fn().mockResolvedValue({ ...desktopStatus, system_vpn_state: 'permission_required' });
    const backend = createWailsBackend(bridge);

    await expect(backend.getConnectionState()).resolves.toMatchObject({
      lifecycle: 'permission_required',
      transportState: 'connected',
      systemVpnState: 'permission_required',
    });
    expect(normalizeNativeStatus({ state: 'connected', active: true, mode: 'proxy', system_vpn_state: 'unknown' })).toMatchObject({
      active: false,
      systemVpnState: 'unsupported',
    });
  });

  it('rejects missing event registrations instead of reporting success', async () => {
    const wails = createWailsBackend(wailsBridge());
    await expect(wails.addListener('statusChanged', vi.fn())).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });

    const plugin = {
      getStatus: vi.fn().mockResolvedValue({ status: 'off' }),
      connect: vi.fn().mockResolvedValue(undefined),
      disconnect: vi.fn().mockResolvedValue(undefined),
      getSocksInfo: vi.fn().mockResolvedValue({ host: '127.0.0.1', port: 1080 }),
      listNodes: vi.fn().mockResolvedValue({ nodes: [] }),
    } as unknown as CapacitorWtTransportPlugin;
    const capacitor = createCapacitorBackendWithNodes(plugin);
    await expect(capacitor.addListener('statusChanged', vi.fn())).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });
    await expect(capacitor.addListener('carrierHealth', vi.fn())).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });

    const legacy = createLegacyDesktopBackend({
      connect: vi.fn().mockResolvedValue(undefined),
      disconnect: vi.fn().mockResolvedValue(undefined),
      refreshDiscovery: vi.fn().mockResolvedValue(undefined),
      startNode: vi.fn().mockResolvedValue(undefined),
      stopNode: vi.fn().mockResolvedValue(undefined),
      getStatus: vi.fn().mockResolvedValue({ ...desktopStatus, status: 'off' }),
      getRuntimeNodes: vi.fn().mockResolvedValue([]),
      onRuntimeStatus: vi.fn(),
    });
    await expect(legacy.addListener('log', vi.fn())).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });
    await expect(legacy.addListener('carrierHealth', vi.fn())).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });
  });

  it('rejects Capacitor log listeners when the native plugin has no log capability', async () => {
    const addListener = vi.fn().mockResolvedValue(undefined);
    const plugin: CapacitorWtTransportPlugin = {
      getStatus: vi.fn().mockResolvedValue({ status: 'off' }),
      connect: vi.fn().mockResolvedValue(undefined),
      disconnect: vi.fn().mockResolvedValue(undefined),
      getSocksInfo: vi.fn().mockResolvedValue({ host: '127.0.0.1', port: 1080 }),
      listNodes: vi.fn().mockResolvedValue({ nodes: [] }),
      addListener,
    };

    await expect(createCapacitorBackendWithNodes(plugin).addListener('log', vi.fn())).rejects.toMatchObject({
      code: 'UNSUPPORTED_CAPABILITY',
      capability: 'log_events',
    });
    expect(addListener).not.toHaveBeenCalled();
  });

  it('rejects missing optional provider methods at the adapter boundary', async () => {
    const plugin = {
      getStatus: vi.fn().mockResolvedValue({ status: 'off' }),
      connect: vi.fn().mockResolvedValue(undefined),
      disconnect: vi.fn().mockResolvedValue(undefined),
      getSocksInfo: vi.fn().mockResolvedValue({ host: '127.0.0.1', port: 1080 }),
      listNodes: undefined,
    } as unknown as CapacitorWtTransportPlugin;
    const capacitor = createCapacitorBackendWithNodes(plugin);
    await expect(capacitor.scanRooms()).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });
    await expect(capacitor.getRuntimeNodes()).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });
    await expect(capacitor.getCarrierHealth?.()).rejects.toMatchObject({ code: 'UNSUPPORTED_CAPABILITY' });
  });
});
