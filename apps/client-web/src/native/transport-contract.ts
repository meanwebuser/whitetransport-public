export type ConnectionLifecycle =
  | 'disconnected'
  | 'permission_required'
  | 'connecting'
  | 'connected'
  | 'degraded'
  | 'disconnecting'
  | 'unsupported'
  | 'error';

export type SystemVPNState =
  | 'unsupported'
  | 'permission_required'
  | 'disconnected'
  | 'connecting'
  | 'connected'
  | 'degraded'
  | 'disconnecting'
  | 'error';
export type NativeHost = 'wails' | 'capacitor' | 'legacy-desktop' | 'browser';
export type CapabilityName =
  | 'system_vpn'
  | 'vpn_permission'
  | 'split_routing'
  | 'proxy_routing'
  | 'logs'
  | 'endpoints'
  | 'transport'
  | 'smoke_test'
  | 'status_events'
  | 'log_events'
  | 'carrier_health_events';

export interface WtCapabilities {
  readonly host: NativeHost;
  readonly transport: boolean;
  readonly endpoints: boolean;
  readonly logs: boolean;
  readonly splitRouting: boolean;
  readonly proxyRouting: boolean;
  readonly systemVpn: boolean;
  readonly requestVpnPermission: boolean;
  readonly startSystemVpn: boolean;
  readonly stopSystemVpn: boolean;
  readonly smokeTest: boolean;
}

export interface WtConnectionState {
  readonly lifecycle: ConnectionLifecycle;
  readonly transportState: string;
  readonly systemVpnState: SystemVPNState;
  readonly selectedNodeId?: string;
  readonly sessionId?: string;
  readonly error?: string;
}

/**
 * Credential-free status for a provider session kept in local native storage.
 *
 * Access/refresh tokens, cookies, headers, and TokenStore data deliberately do
 * not have a representation in this shared bridge contract.
 */
export interface LocalUserAuthSessionStatus {
  readonly provider: string;
  readonly connected: boolean;
  readonly expired: boolean;
  readonly accountLabel?: string;
  readonly expiresAt?: string;
}

/**
 * Project arbitrary native status into the allow-listed local auth contract.
 * Unknown fields are discarded before the value can cross the web/native
 * boundary.
 */
export function normalizeLocalUserAuthSessionStatus(raw: unknown): LocalUserAuthSessionStatus | null {
  if (raw === null || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const source = raw as Record<string, unknown>;
  const provider = typeof source.provider === 'string' ? source.provider.trim().toLowerCase() : '';
  if (!provider) return null;

  const state = typeof source.state === 'string' ? source.state.trim().toLowerCase() : '';
  const expired = source.expired === true || state === 'expired';
  const connected = !expired && (source.connected === true || state === 'connected');
  const accountLabelValue = source.accountLabel ?? source.account_label;
  const expiresAtValue = source.expiresAt ?? source.expires_at;
  const accountLabel = typeof accountLabelValue === 'string' && accountLabelValue.trim() ? accountLabelValue.trim() : undefined;
  const expiresAt = typeof expiresAtValue === 'string' && expiresAtValue.trim() ? expiresAtValue.trim() : undefined;

  return {
    provider,
    connected,
    expired,
    ...(accountLabel ? { accountLabel } : {}),
    ...(expiresAt ? { expiresAt } : {}),
  };
}

export interface WtRoutingSettings {
  readonly mode: string;
  readonly lan_access: boolean;
  /** Optional package identifiers used by native split-routing coordinators. */
  readonly packages?: readonly string[];
  /** Optional CIDR destinations used by desktop system-VPN split routing. */
  readonly destinations?: readonly string[];
}

export interface WtLogInfo {
  readonly path?: string;
  readonly lines: readonly string[];
  readonly persistent: boolean;
}

export interface WtStatus {
  readonly state: string;
  readonly status: string;
  readonly active: boolean;
  readonly mode: 'off' | 'proxy' | 'tunnel';
  readonly transportState?: string;
  readonly systemVpnState?: SystemVPNState;
}

export interface NativeWtStatus {
  readonly state?: string;
  readonly status?: string;
  readonly active?: boolean;
  readonly mode?: 'off' | 'proxy' | 'tunnel';
  readonly transport_state?: string;
  readonly system_vpn_state?: string;
}

export interface WtSocksInfo {
  readonly host: string;
  readonly port: number;
}

export interface RuntimeNode {
  readonly node_id: string;
  readonly available?: boolean;
  readonly reachable?: boolean;
  readonly last_seen_at?: string;
  readonly label?: string;
  readonly country?: string;
  readonly region?: string;
  readonly latency_ms?: number;
  readonly latencyMs?: number;
  readonly capabilities?: readonly string[];
}

export interface CarrierHealthSnapshot {
  readonly healthy?: boolean;
  readonly last_error?: string;
  readonly read_successes?: number;
  readonly read_failures?: number;
  readonly write_successes?: number;
  readonly write_failures?: number;
  readonly last_read_success_at?: string;
  readonly last_write_success_at?: string;
  readonly last_read_failure_at?: string;
  readonly last_write_failure_at?: string;
  readonly [key: string]: unknown;
}

export interface SmokeTestStep {
  readonly name: string;
  readonly status: 'pass' | 'fail' | 'skip';
  readonly durationMs: number;
  readonly detail?: string;
  readonly error?: string;
}

export interface SmokeTestResult {
  readonly passed: boolean;
  readonly totalDurationMs: number;
  readonly steps: readonly SmokeTestStep[];
  readonly summary: string;
  readonly directIp?: string;
  readonly externalIp?: string;
  readonly socksIp?: string;
  readonly tunnelIp?: string;
  readonly latencyMs?: number;
  readonly selectedNodeId?: string;
  readonly logPath?: string;
  readonly resultPath?: string;
}

export interface DesktopRuntimeStatus {
  readonly status: string;
  readonly socksHost: string;
  readonly socksPort: number;
  readonly runtimeKind: 'desktop-joiner' | 'whitetransportd' | 'none';
  readonly runtimePath?: string;
  readonly configPath?: string;
  readonly tokenStorePath?: string;
  readonly logFilePath?: string;
  readonly daemonState?: string;
  readonly diagnosticPaths?: readonly { readonly label: string; readonly path: string; readonly exists: boolean }[];
  readonly pid?: number;
  readonly planner?: unknown;
  readonly message: string;
  readonly error?: string;
  readonly logs: readonly string[];
  readonly directIp?: string;
  readonly externalIp?: string;
  readonly socksIp?: string;
  readonly tunnelIp?: string;
  readonly latencyMs?: number;
}

export type StatusListener = (data: WtStatus) => void;
export type LogListener = (payload: { message: string }) => void;
export type CarrierHealthListener = (snapshot: Record<string, CarrierHealthSnapshot>) => void;

export interface WtTransportBackend {
  getStatus(): Promise<WtStatus>;
  getConnectionState(): Promise<WtConnectionState>;
  getLocalUserAuthSessionStatus?(): Promise<LocalUserAuthSessionStatus | null>;
  getCapabilities(): Promise<WtCapabilities>;
  requestVPNPermission(): Promise<void>;
  startSystemVPN(): Promise<void>;
  stopSystemVPN(): Promise<void>;
  getSplitRouting(): Promise<WtRoutingSettings>;
  setSplitRouting(settings: WtRoutingSettings): Promise<WtRoutingSettings>;
  getProxyRouting(): Promise<WtRoutingSettings>;
  setProxyRouting(settings: WtRoutingSettings): Promise<WtRoutingSettings>;
  getLogInfo(): Promise<WtLogInfo>;
  getDesktopStatus?(): Promise<DesktopRuntimeStatus>;
  connect(options?: { serverId?: string }): Promise<void>;
  disconnect(): Promise<void>;
  setMode(options: { mode: 'off' | 'proxy' | 'tunnel' }): Promise<void>;
  getSocksInfo(): Promise<WtSocksInfo>;
  scanRooms(): Promise<void>;
  getRuntimeNodes(): Promise<RuntimeNode[]>;
  getCarrierHealth?(): Promise<Record<string, CarrierHealthSnapshot>>;
  getDaemonLogs?(): Promise<string[]>;
  runSmokeTest?(options?: { nodeId?: string }): Promise<SmokeTestResult>;
  restartRuntime?(): Promise<void>;
  installRuntimeConfig?(options: { configJson: string }): Promise<unknown>;
  importRuntimeConfigFromDeviceFile?(options: { path: string }): Promise<unknown>;
  getRuntimeConfigStatus?(): Promise<unknown>;
  clearRuntimeConfig?(): Promise<unknown>;
  addListener(eventName: 'statusChanged', listener: StatusListener): Promise<void>;
  addListener(eventName: 'log', listener: LogListener): Promise<void>;
  addListener(eventName: 'carrierHealth', listener: CarrierHealthListener): Promise<void>;
}

export interface CapacitorWtTransportPlugin {
  getStatus(): Promise<NativeWtStatus>;
  getLogInfo?(): Promise<WtLogInfo>;
  getConnectionState?(): Promise<NativeWtStatus>;
  getLocalUserAuthSessionStatus?(): Promise<unknown>;
  beginRoomAuth?(): Promise<{ opened?: boolean }>;
  getRoomAuthStatus?(): Promise<{ ready?: boolean }>;
  getCapabilities?(): Promise<Partial<WtCapabilities>>;
  requestVPNPermission?(): Promise<unknown>;
  startSystemVPN?(): Promise<unknown>;
  stopSystemVPN?(): Promise<unknown>;
  getSplitRouting?(): Promise<WtRoutingSettings>;
  setSplitRouting?(settings: WtRoutingSettings): Promise<WtRoutingSettings>;
  connect(options?: { serverId?: string; configJson?: string }): Promise<unknown>;
  disconnect(): Promise<unknown>;
  setMode?(options: { mode: 'off' | 'proxy' | 'tunnel' }): Promise<void>;
  getSocksInfo(): Promise<WtSocksInfo>;
  listNodes(options: { apiUrl: string }): Promise<{ nodes?: RuntimeNode[] }>;
  scanRooms?(): Promise<void>;
  getCarrierHealth?(): Promise<Record<string, CarrierHealthSnapshot>>;
  installRuntimeConfig?(options: { configJson: string }): Promise<unknown>;
  importRuntimeConfigFromDeviceFile?(options: { path: string }): Promise<unknown>;
  getRuntimeConfigStatus?(): Promise<unknown>;
  clearRuntimeConfig?(): Promise<unknown>;
  addListener?(eventName: 'statusChanged' | 'log', listener: StatusListener | LogListener): Promise<void>;
}

/**
 * WailsAppBridge mirrors the currently generated request/response bindings.
 *
 * Host-backed capability and system-VPN methods are optional while older
 * generated Wails bindings are still in circulation. The adapter must report
 * unsupported for a missing method instead of inventing support.
 */
export interface WailsAppBridge {
  GetStatus(): Promise<Record<string, unknown>>;
  ListServers(): Promise<readonly Record<string, unknown>[]>;
  Connect(serverId: string): Promise<Record<string, unknown>>;
  Disconnect(): Promise<Record<string, unknown>>;
  GetTelemetry(): Promise<Record<string, unknown>>;
  GetLocalUserAuthSessionStatus?(): Promise<unknown>;
  HasClientRoomCredentials?(): Promise<boolean>;
  GetClientCredentialRefreshMessage?(): Promise<string>;
  BeginRoomAuth?(): Promise<{ state: string; message: string }>;
  GetRoomAuthStatus?(): Promise<{ state: string; message: string }>;
  GetLogFilePath(): Promise<string>;
  ReadLogs(limit: number): Promise<readonly Record<string, unknown>[]>;
  GetRoutingSettings(): Promise<WtRoutingSettings>;
  SetRoutingSettings(mode: string, lanAccess: boolean): Promise<WtRoutingSettings>;
  GetCapabilities?(): Promise<Record<string, unknown>>;
  RequestSystemVPNPermission?(): Promise<unknown>;
  StartSystemVPN?(): Promise<unknown>;
  StopSystemVPN?(): Promise<unknown>;
  GetSplitRoutingSettings?(): Promise<WtRoutingSettings>;
  SetSplitRoutingSettings?(mode: string, lanAccess: boolean, destinations: readonly string[]): Promise<WtRoutingSettings>;
  RunTestMode?(serverId: string): Promise<Record<string, unknown>>;
}

export class UnsupportedCapabilityError extends Error {
  readonly code = 'UNSUPPORTED_CAPABILITY';
  readonly capability: CapabilityName;

  constructor(capability: CapabilityName, host: NativeHost) {
    super(`${capability} is unsupported on ${host}`);
    this.capability = capability;
    this.name = 'UnsupportedCapabilityError';
  }
}

export class EndpointUnavailableError extends Error {
  readonly code = 'ENDPOINT_UNAVAILABLE';
  readonly endpoint: 'socks';

  constructor(endpoint: 'socks', host: NativeHost, detail: string) {
    super(`${endpoint} endpoint unavailable on ${host}: ${detail}`);
    this.endpoint = endpoint;
    this.name = 'EndpointUnavailableError';
  }
}

export function mapConnectionLifecycle(status: string): ConnectionLifecycle {
  switch (status.trim().toLowerCase()) {
    case 'permission_required':
    case 'permission-required':
      return 'permission_required';
    case 'connecting':
    case 'starting':
    case 'discovering':
    case 'reconnecting':
      return 'connecting';
    case 'connected':
    case 'running':
    case 'tunnel_active':
      return 'connected';
    case 'degraded':
    case 'unhealthy':
      return 'degraded';
    case 'disconnecting':
      return 'disconnecting';
    case 'unsupported':
      return 'unsupported';
    case 'error':
    case 'failed':
      return 'error';
    case '':
    case 'off':
    case 'stopped':
    case 'disconnected':
    case 'call_disconnected':
      return 'disconnected';
    default:
      return 'error';
  }
}

export function normalizeSystemVPNState(value: unknown): SystemVPNState {
  switch (typeof value === 'string' ? value.trim().toLowerCase() : '') {
    case 'permission_required':
    case 'permission-required':
      return 'permission_required';
    case 'disconnected':
      return 'disconnected';
    case 'connecting':
      return 'connecting';
    case 'connected':
      return 'connected';
    case 'degraded':
      return 'degraded';
    case 'disconnecting':
      return 'disconnecting';
    case 'error':
      return 'error';
    case 'unsupported':
      return 'unsupported';
    default:
      return 'unsupported';
  }
}

export function normalizeNativeStatus(nativeStatus: NativeWtStatus): WtStatus {
  const status = nativeStatus.status ?? nativeStatus.state;
  if (!status) throw new Error('Native transport status must include state or status');
  const transportState = nativeStatus.transport_state ?? status;
  const systemVpnState = normalizeSystemVPNState(nativeStatus.system_vpn_state);
  const unifiedActive = mapConnectionLifecycle(transportState) === 'connected' && systemVpnState === 'connected';
  return {
    ...nativeStatus,
    state: nativeStatus.state ?? status,
    status,
    active: unifiedActive,
    mode: nativeStatus.mode ?? (mapConnectionLifecycle(status) === 'connected' ? 'proxy' : 'off'),
    transportState,
    systemVpnState,
  };
}

export function connectionStateFromStatus(status: WtStatus): WtConnectionState {
  const transportLifecycle = mapConnectionLifecycle(status.transportState ?? status.status);
  const systemState = status.systemVpnState ?? 'unsupported';
  let lifecycle = transportLifecycle;
  if (systemState === 'permission_required') lifecycle = 'permission_required';
  else if (systemState === 'error') lifecycle = 'error';
  else if (systemState === 'disconnecting') lifecycle = 'disconnecting';
  else if (transportLifecycle === 'connected') {
    if (systemState === 'connected') lifecycle = 'connected';
    else if (systemState === 'connecting') lifecycle = 'connecting';
    else lifecycle = 'degraded';
  }
  return {
    lifecycle,
    transportState: status.transportState ?? status.status,
    systemVpnState: systemState,
  };
}
