import { Capacitor, registerPlugin } from '@capacitor/core';
import { createWailsBackend, getWailsApp, hasWailsHost } from './wails-transport';
import { normalizeLocalUserAuthSessionStatus, UnsupportedCapabilityError } from './transport-contract';
import {
  connectionStateFromStatus,
  normalizeNativeStatus,
  type CarrierHealthListener,
  type CarrierHealthSnapshot,
  type CapacitorWtTransportPlugin,
  type CapabilityName,
  type DesktopRuntimeStatus,
  type LogListener,
  type LocalUserAuthSessionStatus,
  type NativeHost,
  type NativeWtStatus,
  type RuntimeNode,
  type SmokeTestResult,
  type StatusListener,
  type WtCapabilities,
  type WtConnectionState,
  type WtLogInfo,
  type WtRoutingSettings,
  type WtSocksInfo,
  type WtStatus,
  type WtTransportBackend,
} from './transport-contract';
export * from './transport-contract';

export interface LegacyDesktopBridge {
  connect(nodeId?: string): Promise<unknown>;
  disconnect(): Promise<unknown>;
  refreshDiscovery(): Promise<unknown>;
  startNode(nodeId?: string): Promise<unknown>;
  stopNode(): Promise<unknown>;
  getStatus(): Promise<DesktopRuntimeStatus>;
  getRuntimeNodes(): Promise<RuntimeNode[]>;
  getCarrierHealth?(): Promise<Record<string, CarrierHealthSnapshot>>;
  getDaemonLogs?(): Promise<string[]>;
  runSmokeTest?(nodeId?: string): Promise<SmokeTestResult>;
  restartRuntime?(): Promise<DesktopRuntimeStatus>;
  onNodeStatus?(callback: (status: DesktopRuntimeStatus) => void): void;
  onRuntimeStatus(callback: (status: DesktopRuntimeStatus) => void): void;
  onLog?(callback: (payload: { message: string }) => void): void;
  onCarrierHealth?(callback: (snapshot: Record<string, CarrierHealthSnapshot>) => void): void;
}

declare global {
  interface Window {
    ytp?: LegacyDesktopBridge;
  }
}

const CapacitorBackend = registerPlugin<CapacitorWtTransportPlugin>('WtTransport');

const isActive = (status: string): boolean => status === 'connected' || status === 'running' || status === 'degraded';

const legacyDesktopStatus = (status: DesktopRuntimeStatus): WtStatus => ({
  state: status.status,
  status: status.status,
  active: false,
  mode: isActive(status.status) ? 'proxy' : 'off',
  transportState: status.status,
  systemVpnState: 'unsupported',
});

export function createLegacyDesktopBackend(bridge: LegacyDesktopBridge): WtTransportBackend {
  const backend: WtTransportBackend = {
    async getStatus(): Promise<WtStatus> {
      return legacyDesktopStatus(await bridge.getStatus());
    },
    async getDesktopStatus(): Promise<DesktopRuntimeStatus> {
      return bridge.getStatus();
    },
    async getConnectionState(): Promise<WtConnectionState> {
      return connectionStateFromStatus(await backend.getStatus());
    },
    async getCapabilities(): Promise<WtCapabilities> {
      return {
        host: 'legacy-desktop',
        transport: true,
        endpoints: true,
        logs: Boolean(bridge.getDaemonLogs),
        splitRouting: false,
        proxyRouting: false,
        systemVpn: false,
        requestVpnPermission: false,
        startSystemVpn: false,
        stopSystemVpn: false,
        smokeTest: Boolean(bridge.runSmokeTest),
      };
    },
    async requestVPNPermission(): Promise<void> {
      throw new UnsupportedCapabilityError('vpn_permission', 'legacy-desktop');
    },
    async startSystemVPN(): Promise<void> {
      throw new UnsupportedCapabilityError('system_vpn', 'legacy-desktop');
    },
    async stopSystemVPN(): Promise<void> {
      throw new UnsupportedCapabilityError('system_vpn', 'legacy-desktop');
    },
    async getSplitRouting(): Promise<WtRoutingSettings> {
      throw new UnsupportedCapabilityError('split_routing', 'legacy-desktop');
    },
    async setSplitRouting(): Promise<WtRoutingSettings> {
      throw new UnsupportedCapabilityError('split_routing', 'legacy-desktop');
    },
    async getProxyRouting(): Promise<WtRoutingSettings> {
      throw new UnsupportedCapabilityError('proxy_routing', 'legacy-desktop');
    },
    async setProxyRouting(): Promise<WtRoutingSettings> {
      throw new UnsupportedCapabilityError('proxy_routing', 'legacy-desktop');
    },
    async getLogInfo(): Promise<WtLogInfo> {
      const status = await bridge.getStatus();
      if (!bridge.getDaemonLogs) throw new UnsupportedCapabilityError('logs', 'legacy-desktop');
      const lines = await bridge.getDaemonLogs();
      return { path: status.logFilePath, lines, persistent: Boolean(status.logFilePath) };
    },
    async connect(options?: { serverId?: string }): Promise<void> {
      await bridge.connect(options?.serverId);
    },
    async disconnect(): Promise<void> {
      await bridge.disconnect();
    },
    async setMode(): Promise<void> {
      throw new UnsupportedCapabilityError('system_vpn', 'legacy-desktop');
    },
    async getSocksInfo(): Promise<WtSocksInfo> {
      const status = await bridge.getStatus();
      return { host: status.socksHost, port: status.socksPort };
    },
    async scanRooms(): Promise<void> {
      await bridge.refreshDiscovery();
    },
    async getRuntimeNodes(): Promise<RuntimeNode[]> {
      return bridge.getRuntimeNodes();
    },
    async getCarrierHealth(): Promise<Record<string, CarrierHealthSnapshot>> {
      if (!bridge.getCarrierHealth) throw new UnsupportedCapabilityError('carrier_health_events', 'legacy-desktop');
      return bridge.getCarrierHealth();
    },
    async getDaemonLogs(): Promise<string[]> {
      if (!bridge.getDaemonLogs) throw new UnsupportedCapabilityError('logs', 'legacy-desktop');
      return bridge.getDaemonLogs();
    },
    async runSmokeTest(options?: { nodeId?: string }): Promise<SmokeTestResult> {
      if (!bridge.runSmokeTest) throw new UnsupportedCapabilityError('smoke_test', 'legacy-desktop');
      return bridge.runSmokeTest(options?.nodeId);
    },
    async restartRuntime(): Promise<void> {
      if (!bridge.restartRuntime) throw new UnsupportedCapabilityError('transport', 'legacy-desktop');
      await bridge.restartRuntime();
    },
    async addListener(eventName: 'statusChanged' | 'log' | 'carrierHealth', listener: StatusListener | LogListener | CarrierHealthListener): Promise<void> {
      if (eventName === 'statusChanged') {
        if (!bridge.onRuntimeStatus) throw new UnsupportedCapabilityError('status_events', 'legacy-desktop');
        bridge.onRuntimeStatus((status) => {
          (listener as StatusListener)(legacyDesktopStatus(status));
        });
        return;
      }
      if (eventName === 'carrierHealth') {
        if (!bridge.onCarrierHealth) throw new UnsupportedCapabilityError('carrier_health_events', 'legacy-desktop');
        bridge.onCarrierHealth((snapshot) => {
          (listener as CarrierHealthListener)(snapshot);
        });
        return;
      }
      if (!bridge.onLog) throw new UnsupportedCapabilityError('log_events', 'legacy-desktop');
      bridge.onLog((payload) => {
        (listener as LogListener)(payload);
      });
    },
  };
  return backend;
}

type NativeHostRoot = {
  readonly go?: { readonly main?: { readonly App?: unknown } };
  readonly ytp?: LegacyDesktopBridge;
};

const currentRoot = (): NativeHostRoot | undefined => typeof window === 'undefined' ? undefined : window;
const asNativeHostRoot = (root: unknown): NativeHostRoot | undefined => {
  if (root === null || typeof root !== 'object') return undefined;
  return root as NativeHostRoot;
};
const hasElectron = (root: unknown = currentRoot()): boolean => Boolean(asNativeHostRoot(root)?.ytp);
const hasCapacitor = (): boolean => Capacitor.isNativePlatform();

export const isHosted = (): boolean => hasWailsHost() || hasElectron() || hasCapacitor();
export const isDesktopHosted = (): boolean => hasWailsHost() || hasElectron();
export const isCapacitorHosted = (): boolean => hasCapacitor();

export function createCapacitorBackendWithNodes(capacitorBackend: CapacitorWtTransportPlugin): WtTransportBackend {
  const backend: WtTransportBackend = {
    // Capacitor plugins are dynamic proxies: object spread copies no plugin
    // methods. Keep every supported call as an explicit delegate.
    getStatus: async () => normalizeNativeStatus(await capacitorBackend.getStatus()),
    async getConnectionState(): Promise<WtConnectionState> {
      if (capacitorBackend.getConnectionState) {
        return connectionStateFromStatus(normalizeNativeStatus(await capacitorBackend.getConnectionState()));
      }
      return connectionStateFromStatus(await backend.getStatus());
    },
    async getLocalUserAuthSessionStatus(): Promise<LocalUserAuthSessionStatus | null> {
      if (!capacitorBackend.getLocalUserAuthSessionStatus) return null;
      return normalizeLocalUserAuthSessionStatus(await capacitorBackend.getLocalUserAuthSessionStatus());
    },
    async getCapabilities(): Promise<WtCapabilities> {
      if (capacitorBackend.getCapabilities) {
        const capabilities = await capacitorBackend.getCapabilities();
        return {
          host: 'capacitor',
          transport: capabilities.transport === true,
          endpoints: capabilities.endpoints === true,
          logs: capabilities.logs === true,
          splitRouting: capabilities.splitRouting === true,
          proxyRouting: capabilities.proxyRouting === true,
          systemVpn: capabilities.systemVpn === true,
          requestVpnPermission: capabilities.requestVpnPermission === true,
          startSystemVpn: capabilities.startSystemVpn === true,
          stopSystemVpn: capabilities.stopSystemVpn === true,
          smokeTest: capabilities.smokeTest === true,
        };
      }
      return {
        host: 'capacitor',
        transport: true,
        endpoints: true,
        logs: false,
        splitRouting: false,
        proxyRouting: false,
        systemVpn: false,
        requestVpnPermission: false,
        startSystemVpn: false,
        stopSystemVpn: false,
        smokeTest: false,
      };
    },
    async requestVPNPermission(): Promise<void> {
      if (!capacitorBackend.requestVPNPermission) throw new UnsupportedCapabilityError('vpn_permission', 'capacitor');
      await capacitorBackend.requestVPNPermission();
    },
    async startSystemVPN(): Promise<void> {
      if (!capacitorBackend.startSystemVPN) throw new UnsupportedCapabilityError('system_vpn', 'capacitor');
      await capacitorBackend.startSystemVPN();
    },
    async stopSystemVPN(): Promise<void> {
      if (!capacitorBackend.stopSystemVPN) throw new UnsupportedCapabilityError('system_vpn', 'capacitor');
      await capacitorBackend.stopSystemVPN();
    },
    async getSplitRouting(): Promise<WtRoutingSettings> {
      if (!capacitorBackend.getSplitRouting) throw new UnsupportedCapabilityError('split_routing', 'capacitor');
      return capacitorBackend.getSplitRouting();
    },
    async setSplitRouting(settings: WtRoutingSettings): Promise<WtRoutingSettings> {
      if (!capacitorBackend.setSplitRouting) throw new UnsupportedCapabilityError('split_routing', 'capacitor');
      return capacitorBackend.setSplitRouting(settings);
    },
    async getProxyRouting(): Promise<WtRoutingSettings> {
      throw new UnsupportedCapabilityError('proxy_routing', 'capacitor');
    },
    async setProxyRouting(): Promise<WtRoutingSettings> {
      throw new UnsupportedCapabilityError('proxy_routing', 'capacitor');
    },
    async getLogInfo(): Promise<WtLogInfo> {
      if (!capacitorBackend.getLogInfo) throw new UnsupportedCapabilityError('logs', 'capacitor');
      return capacitorBackend.getLogInfo();
    },
    connect: async (options) => { await capacitorBackend.connect(options); },
    disconnect: async () => { await capacitorBackend.disconnect(); },
    setMode: async (options) => {
      if (!capacitorBackend.setMode) throw new UnsupportedCapabilityError('system_vpn', 'capacitor');
      await capacitorBackend.setMode(options);
    },
    getSocksInfo: () => capacitorBackend.getSocksInfo(),
    scanRooms: async () => {
      if (!capacitorBackend.scanRooms) throw new UnsupportedCapabilityError('endpoints', 'capacitor');
      await capacitorBackend.scanRooms();
    },
    getCarrierHealth: async (): Promise<Record<string, CarrierHealthSnapshot>> => {
      if (!capacitorBackend.getCarrierHealth) throw new UnsupportedCapabilityError('carrier_health_events', 'capacitor');
      return capacitorBackend.getCarrierHealth();
    },
    installRuntimeConfig: (options) => capacitorBackend.installRuntimeConfig ? capacitorBackend.installRuntimeConfig(options) : Promise.reject(new UnsupportedCapabilityError('transport', 'capacitor')),
    importRuntimeConfigFromDeviceFile: (options) => capacitorBackend.importRuntimeConfigFromDeviceFile ? capacitorBackend.importRuntimeConfigFromDeviceFile(options) : Promise.reject(new UnsupportedCapabilityError('transport', 'capacitor')),
    getRuntimeConfigStatus: () => capacitorBackend.getRuntimeConfigStatus ? capacitorBackend.getRuntimeConfigStatus() : Promise.reject(new UnsupportedCapabilityError('transport', 'capacitor')),
    clearRuntimeConfig: () => capacitorBackend.clearRuntimeConfig ? capacitorBackend.clearRuntimeConfig() : Promise.reject(new UnsupportedCapabilityError('transport', 'capacitor')),
    async getRuntimeNodes(): Promise<RuntimeNode[]> {
      if (!capacitorBackend.listNodes) throw new UnsupportedCapabilityError('endpoints', 'capacitor');
      const result = await capacitorBackend.listNodes({ apiUrl: 'http://127.0.0.1:17680' });
      return result.nodes ?? [];
    },
    async addListener(eventName: 'statusChanged' | 'log' | 'carrierHealth', listener: StatusListener | LogListener | CarrierHealthListener): Promise<void> {
      if (eventName === 'carrierHealth') {
        throw new UnsupportedCapabilityError('carrier_health_events', 'capacitor');
      }
      if (eventName === 'log') {
        throw new UnsupportedCapabilityError('log_events', 'capacitor');
      }
      if (!capacitorBackend.addListener) {
        throw new UnsupportedCapabilityError('status_events', 'capacitor');
      }
      await capacitorBackend.addListener('statusChanged', (nativeStatus: NativeWtStatus) => {
        (listener as StatusListener)(normalizeNativeStatus(nativeStatus));
      });
    },
  };
  return backend;
}

const CapacitorBackendWithNodes = createCapacitorBackendWithNodes(CapacitorBackend);

const browserUnsupported = (capability: CapabilityName): never => {
  throw new UnsupportedCapabilityError(capability, 'browser');
};

const BrowserBackend: WtTransportBackend = {
  getStatus: async () => browserUnsupported('transport'),
  getConnectionState: async () => browserUnsupported('transport'),
  getCapabilities: async () => ({
    host: 'browser',
    transport: false,
    endpoints: false,
    logs: false,
    splitRouting: false,
    proxyRouting: false,
    systemVpn: false,
    requestVpnPermission: false,
    startSystemVpn: false,
    stopSystemVpn: false,
    smokeTest: false,
  }),
  requestVPNPermission: async () => browserUnsupported('vpn_permission'),
  startSystemVPN: async () => browserUnsupported('system_vpn'),
  stopSystemVPN: async () => browserUnsupported('system_vpn'),
  getSplitRouting: async () => browserUnsupported('split_routing'),
  setSplitRouting: async () => browserUnsupported('split_routing'),
  getProxyRouting: async () => browserUnsupported('proxy_routing'),
  setProxyRouting: async () => browserUnsupported('proxy_routing'),
  getLogInfo: async () => browserUnsupported('logs'),
  connect: async () => browserUnsupported('transport'),
  disconnect: async () => browserUnsupported('transport'),
  setMode: async () => browserUnsupported('system_vpn'),
  getSocksInfo: async () => browserUnsupported('endpoints'),
  scanRooms: async () => browserUnsupported('endpoints'),
  getRuntimeNodes: async () => browserUnsupported('endpoints'),
  addListener: async (eventName) => browserUnsupported(eventName === 'statusChanged' ? 'status_events' : eventName === 'log' ? 'log_events' : 'carrier_health_events'),
};

export function resolveTransportBackend(root: unknown = currentRoot()): WtTransportBackend {
  if (hasWailsHost(root)) {
    const app = getWailsApp(root);
    if (!app) throw new Error('Wails host detected without generated App bridge');
    return createWailsBackend(app);
  }
  const candidate = asNativeHostRoot(root);
  if (candidate?.ytp) return createLegacyDesktopBackend(candidate.ytp);
  if (hasCapacitor()) return CapacitorBackendWithNodes;
  return BrowserBackend;
}

function currentHost(): NativeHost {
  const root = currentRoot();
  if (hasWailsHost(root)) return 'wails';
  if (hasElectron(root)) return 'legacy-desktop';
  if (hasCapacitor()) return 'capacitor';
  return 'browser';
}

/**
 * Legacy callers still receive empty collections for optional telemetry APIs.
 * New contract methods reject at the adapter boundary; this compatibility
 * wrapper is deliberately kept only for the existing store's optional fields.
 */
async function optionalLegacyValue<T>(method: (() => Promise<T>) | undefined, fallback: T): Promise<T> {
  if (!method) return fallback;
  try {
    return await method();
  } catch (error) {
    if (error instanceof UnsupportedCapabilityError) return fallback;
    throw error;
  }
}

const WtTransport = {
  getStatus: (): Promise<WtStatus> => resolveTransportBackend().getStatus(),
  getConnectionState: (): Promise<WtConnectionState> => resolveTransportBackend().getConnectionState(),
  getCapabilities: (): Promise<WtCapabilities> => resolveTransportBackend().getCapabilities(),
  requestVPNPermission: (): Promise<void> => resolveTransportBackend().requestVPNPermission(),
  startSystemVPN: (): Promise<void> => resolveTransportBackend().startSystemVPN(),
  stopSystemVPN: (): Promise<void> => resolveTransportBackend().stopSystemVPN(),
  getSplitRouting: (): Promise<WtRoutingSettings> => resolveTransportBackend().getSplitRouting(),
  setSplitRouting: (settings: WtRoutingSettings): Promise<WtRoutingSettings> => resolveTransportBackend().setSplitRouting(settings),
  getProxyRouting: (): Promise<WtRoutingSettings> => resolveTransportBackend().getProxyRouting(),
  setProxyRouting: (settings: WtRoutingSettings): Promise<WtRoutingSettings> => resolveTransportBackend().setProxyRouting(settings),
  getLogInfo: (): Promise<WtLogInfo> => resolveTransportBackend().getLogInfo(),
  /** @deprecated Optional desktop telemetry compatibility for the existing store. */
  getDesktopStatus: (): Promise<DesktopRuntimeStatus | null> => {
    const backend = resolveTransportBackend();
    return backend.getDesktopStatus ? backend.getDesktopStatus() : Promise.resolve(null);
  },
  connect: (options?: { serverId?: string }): Promise<void> => resolveTransportBackend().connect(options),
  disconnect: (): Promise<void> => resolveTransportBackend().disconnect(),
  setMode: (options: { mode: 'off' | 'proxy' | 'tunnel' }): Promise<void> => resolveTransportBackend().setMode(options),
  getSocksInfo: (): Promise<WtSocksInfo> => resolveTransportBackend().getSocksInfo(),
  scanRooms: (): Promise<void> => resolveTransportBackend().scanRooms(),
  getRuntimeNodes: (): Promise<RuntimeNode[]> => resolveTransportBackend().getRuntimeNodes(),
  /** @deprecated Optional telemetry compatibility for legacy store fields. */
  getCarrierHealth: (): Promise<Record<string, CarrierHealthSnapshot>> => {
    const backend = resolveTransportBackend();
    return optionalLegacyValue(backend.getCarrierHealth, {});
  },
  /** @deprecated Optional telemetry compatibility for legacy store fields. */
  getDaemonLogs: (): Promise<string[]> => {
    const backend = resolveTransportBackend();
    return optionalLegacyValue(backend.getDaemonLogs, []);
  },
  runSmokeTest: (options?: { nodeId?: string }): Promise<SmokeTestResult> => {
    const backend = resolveTransportBackend();
    if (!backend.runSmokeTest) return Promise.reject(new UnsupportedCapabilityError('smoke_test', currentHost()));
    return backend.runSmokeTest(options);
  },
  restartRuntime: (): Promise<void> => {
    const backend = resolveTransportBackend();
    if (!backend.restartRuntime) return Promise.reject(new UnsupportedCapabilityError('transport', currentHost()));
    return backend.restartRuntime();
  },
  installRuntimeConfig: (configJson: string): Promise<unknown> => {
    const backend = resolveTransportBackend();
    if (!backend.installRuntimeConfig) return Promise.reject(new UnsupportedCapabilityError('transport', currentHost()));
    return backend.installRuntimeConfig({ configJson });
  },
  importRuntimeConfigFromDeviceFile: (path: string): Promise<unknown> => {
    const backend = resolveTransportBackend();
    if (!backend.importRuntimeConfigFromDeviceFile) return Promise.reject(new UnsupportedCapabilityError('transport', currentHost()));
    return backend.importRuntimeConfigFromDeviceFile({ path });
  },
  getRuntimeConfigStatus: (): Promise<unknown> => {
    const backend = resolveTransportBackend();
    if (!backend.getRuntimeConfigStatus) return Promise.reject(new UnsupportedCapabilityError('transport', currentHost()));
    return backend.getRuntimeConfigStatus();
  },
  clearRuntimeConfig: (): Promise<unknown> => {
    const backend = resolveTransportBackend();
    if (!backend.clearRuntimeConfig) return Promise.reject(new UnsupportedCapabilityError('transport', currentHost()));
    return backend.clearRuntimeConfig();
  },
  addListener: async (eventName: 'statusChanged' | 'log' | 'carrierHealth', listener: StatusListener | LogListener | CarrierHealthListener): Promise<void> => {
    const backend = resolveTransportBackend();
    if (eventName === 'statusChanged') {
      await backend.addListener('statusChanged', listener as StatusListener);
      return;
    }
    if (eventName === 'carrierHealth') {
      await backend.addListener('carrierHealth', listener as CarrierHealthListener);
      return;
    }
    await backend.addListener('log', listener as LogListener);
  },
};

export { createWailsBackend, getWailsApp, hasWailsHost };
export default WtTransport;
