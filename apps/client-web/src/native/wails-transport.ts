import {
  connectionStateFromStatus,
  EndpointUnavailableError,
  mapConnectionLifecycle,
  normalizeLocalUserAuthSessionStatus,
  normalizeSystemVPNState,
  UnsupportedCapabilityError,
  type DesktopRuntimeStatus,
  type RuntimeNode,
  type SmokeTestResult,
  type WailsAppBridge,
  type WtCapabilities,
  type CapabilityName,
  type WtConnectionState,
  type LocalUserAuthSessionStatus,
  type WtLogInfo,
  type WtRoutingSettings,
  type WtStatus,
  type WtTransportBackend,
} from './transport-contract.ts';

declare global {
  interface Window {
    go?: {
      main?: {
        App?: WailsAppBridge;
      };
    };
  }
}

/** Detect the generated Wails bridge without requiring a Wails-only import. */
export function hasWailsHost(root: unknown = typeof window === 'undefined' ? undefined : window): boolean {
  const candidate = root as { go?: { main?: { App?: unknown } } } | undefined;
  return Boolean(candidate?.go?.main?.App);
}

export function getWailsApp(root: unknown = typeof window === 'undefined' ? undefined : window): WailsAppBridge | null {
  const candidate = root as { go?: { main?: { App?: WailsAppBridge } } } | undefined;
  return candidate?.go?.main?.App ?? null;
}

const stringValue = (value: unknown): string => typeof value === 'string' ? value : '';

function parseSocksAddress(value: string): { host: string; port: number } | null {
  const trimmed = value.trim();
  const splitAt = trimmed.lastIndexOf(':');
  if (splitAt <= 0) return null;
  const host = trimmed.slice(0, splitAt).trim();
  const portText = trimmed.slice(splitAt + 1);
  if (!/^\d+$/.test(portText)) return null;
  const port = Number.parseInt(portText, 10);
  if (!host || !Number.isInteger(port) || port < 1 || port > 65535) return null;
  return { host, port };
}

function requireSocksAddress(value: unknown): { host: string; port: number } {
  const endpoint = parseSocksAddress(stringValue(value));
  if (!endpoint) throw new EndpointUnavailableError('socks', 'wails', 'runtime status has no valid host:port');
  return endpoint;
}

function mapWailsStatus(raw: Record<string, unknown>): WtStatus {
  const status = stringValue(raw.transport_state) || stringValue(raw.state) || stringValue(raw.runtime_state) || stringValue(raw.status) || 'disconnected';
  const transportState = stringValue(raw.transport_state) || status;
  const lifecycle = mapConnectionLifecycle(transportState);
  const systemVpnState = normalizeSystemVPNState(raw.system_vpn_state);
  const connected = lifecycle === 'connected' && systemVpnState === 'connected';
  return {
    state: status,
    status,
    active: connected,
    mode: connected ? 'proxy' : 'off',
    transportState,
    systemVpnState,
  };
}

function mapWailsNode(raw: Record<string, unknown>): RuntimeNode {
  return {
    node_id: stringValue(raw.id) || stringValue(raw.node_id),
    label: stringValue(raw.label),
    country: stringValue(raw.country),
    region: stringValue(raw.region),
    available: raw.available === true,
    latency_ms: typeof raw.latency_ms === 'number' ? raw.latency_ms : undefined,
    last_seen_at: stringValue(raw.last_seen_at),
    capabilities: Array.isArray(raw.capabilities)
      ? raw.capabilities.filter((value): value is string => typeof value === 'string')
      : undefined,
  };
}

function mapWailsDesktopStatus(raw: Record<string, unknown>, telemetry: Record<string, unknown>): DesktopRuntimeStatus {
  const socks = parseSocksAddress(stringValue(raw.socks_listen));
  const productState = stringValue(raw.state) || stringValue(raw.status) || 'disconnected';
  const daemonState = stringValue(raw.transport_state) || stringValue(raw.runtime_state) || 'unknown';
  const latencyMs = typeof telemetry.latency_ms === 'number' && Number.isFinite(telemetry.latency_ms) ? telemetry.latency_ms : undefined;
  return {
    status: productState,
    socksHost: socks?.host ?? '',
    socksPort: socks?.port ?? 0,
    runtimeKind: 'whitetransportd',
    logFilePath: undefined,
    daemonState,
    message: stringValue(raw.message),
    error: stringValue(telemetry.error) || undefined,
    logs: [],
    directIp: undefined,
    externalIp: stringValue(telemetry.external_ip) || undefined,
    latencyMs,
  };
}

const unsupported = (capability: CapabilityName): never => {
  throw new UnsupportedCapabilityError(capability, 'wails');
};

function capabilityFlag(raw: Record<string, unknown>, camelName: string, snakeName: string): boolean {
  return raw[camelName] === true || raw[snakeName] === true;
}

export function createWailsBackend(app: WailsAppBridge): WtTransportBackend {
  const backend: WtTransportBackend = {
    async getStatus(): Promise<WtStatus> {
      return mapWailsStatus(await app.GetStatus());
    },
    async getConnectionState(): Promise<WtConnectionState> {
      const raw = await app.GetStatus();
      const state = connectionStateFromStatus(mapWailsStatus(raw));
      return {
        ...state,
        selectedNodeId: stringValue(raw.active_node_id) || undefined,
        sessionId: stringValue(raw.session_id) || undefined,
        error: stringValue(raw.last_runtime_error) || undefined,
      };
    },
    async getLocalUserAuthSessionStatus(): Promise<LocalUserAuthSessionStatus | null> {
      if (!app.GetLocalUserAuthSessionStatus) return null;
      return normalizeLocalUserAuthSessionStatus(await app.GetLocalUserAuthSessionStatus());
    },
    async getCapabilities(): Promise<WtCapabilities> {
      if (app.GetCapabilities) {
        const raw = await app.GetCapabilities();
        return {
          host: 'wails',
          transport: capabilityFlag(raw, 'transport', 'transport'),
          endpoints: capabilityFlag(raw, 'endpoints', 'endpoints'),
          logs: capabilityFlag(raw, 'logs', 'logs'),
          splitRouting: capabilityFlag(raw, 'splitRouting', 'split_routing'),
          proxyRouting: capabilityFlag(raw, 'proxyRouting', 'proxy_routing'),
          systemVpn: capabilityFlag(raw, 'systemVpn', 'system_vpn'),
          requestVpnPermission: capabilityFlag(raw, 'requestVpnPermission', 'request_vpn_permission'),
          startSystemVpn: capabilityFlag(raw, 'startSystemVpn', 'start_system_vpn'),
          stopSystemVpn: capabilityFlag(raw, 'stopSystemVpn', 'stop_system_vpn'),
          // Keep the capability honest when an older generated Wails bridge
          // lacks the bound action even if its host advertises stale metadata.
          smokeTest: capabilityFlag(raw, 'smokeTest', 'smoke_test') && Boolean(app.RunTestMode),
        };
      }
      return {
        host: 'wails',
        transport: true,
        endpoints: true,
        logs: true,
        splitRouting: false,
        proxyRouting: true,
        systemVpn: false,
        requestVpnPermission: false,
        startSystemVpn: false,
        stopSystemVpn: false,
        smokeTest: false,
      };
    },
    async requestVPNPermission(): Promise<void> {
      if (!app.RequestSystemVPNPermission) return unsupported('vpn_permission');
      await app.RequestSystemVPNPermission();
    },
    async startSystemVPN(): Promise<void> {
      if (!app.StartSystemVPN) return unsupported('system_vpn');
      await app.StartSystemVPN();
    },
    async stopSystemVPN(): Promise<void> {
      if (!app.StopSystemVPN) return unsupported('system_vpn');
      await app.StopSystemVPN();
    },
    async getSplitRouting(): Promise<WtRoutingSettings> {
      if (!app.GetSplitRoutingSettings) return unsupported('split_routing');
      return app.GetSplitRoutingSettings();
    },
    async setSplitRouting(settings: WtRoutingSettings): Promise<WtRoutingSettings> {
      if (!app.SetSplitRoutingSettings) return unsupported('split_routing');
      return app.SetSplitRoutingSettings(settings.mode, settings.lan_access, settings.destinations ?? []);
    },
    async getProxyRouting(): Promise<WtRoutingSettings> {
      return app.GetRoutingSettings();
    },
    async setProxyRouting(settings: WtRoutingSettings): Promise<WtRoutingSettings> {
      return app.SetRoutingSettings(settings.mode, settings.lan_access);
    },
    async getLogInfo(): Promise<WtLogInfo> {
      const [path, records] = await Promise.all([app.GetLogFilePath(), app.ReadLogs(200)]);
      return {
        path,
        lines: records.map((record) => stringValue(record.message)),
        persistent: Boolean(path),
      };
    },
    async getDesktopStatus(): Promise<DesktopRuntimeStatus> {
      const [status, telemetry] = await Promise.all([app.GetStatus(), app.GetTelemetry()]);
      return mapWailsDesktopStatus(status, telemetry);
    },
    async connect(options?: { serverId?: string }): Promise<void> {
      await app.Connect(options?.serverId ?? '');
    },
    async disconnect(): Promise<void> {
      await app.Disconnect();
    },
    async setMode(): Promise<void> {
      return unsupported('system_vpn');
    },
    async getSocksInfo() {
      const status = await app.GetStatus();
      return requireSocksAddress(status.socks_listen);
    },
    async scanRooms(): Promise<void> {
      await app.ListServers();
    },
    async getRuntimeNodes(): Promise<RuntimeNode[]> {
      return (await app.ListServers()).map(mapWailsNode);
    },
    async getCarrierHealth() {
      return unsupported('carrier_health_events');
    },
    async getDaemonLogs(): Promise<string[]> {
      const records = await app.ReadLogs(200);
      return records.map((record) => stringValue(record.message));
    },
    async runSmokeTest(options?: { nodeId?: string }): Promise<SmokeTestResult> {
      if (!app.RunTestMode) return unsupported('smoke_test');
      const result = await app.RunTestMode(options?.nodeId ?? '');
      const testResult = result as { readonly passed?: unknown; readonly error?: unknown; readonly activeNodeId?: unknown; readonly externalIp?: unknown };
      const error = stringValue(testResult.error);
      return {
        passed: testResult.passed === true,
        totalDurationMs: 0,
        summary: error || (testResult.passed === true ? 'GUI test mode passed' : 'GUI test mode failed'),
        selectedNodeId: stringValue(testResult.activeNodeId) || undefined,
        directIp: stringValue(testResult.externalIp) || undefined,
        logPath: stringValue((result as { readonly logPath?: unknown }).logPath) || undefined,
        resultPath: stringValue((result as { readonly resultPath?: unknown }).resultPath) || undefined,
        steps: [{ name: 'connect / telemetry / cleanup', status: testResult.passed === true ? 'pass' : 'fail', durationMs: 0, error: error || undefined }],
      };
    },
    async restartRuntime(): Promise<void> {
      return unsupported('transport');
    },
    async addListener(eventName): Promise<void> {
      // The currently generated Wails bridge has no event subscription
      // surface. Do not report success for a listener that will never fire.
      const capability = eventName === 'log' ? 'log_events' : eventName === 'carrierHealth' ? 'carrier_health_events' : 'status_events';
      return unsupported(capability);
    },
  };
  return backend;
}
