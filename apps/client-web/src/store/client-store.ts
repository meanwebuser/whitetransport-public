import { create } from 'zustand';

import WtTransport, {
  isDesktopHosted,
  isHosted,
  type CarrierHealthSnapshot,
  type DesktopRuntimeStatus,
  type RuntimeNode,
  type SmokeTestResult,
} from '../native/wt-transport';

export type TransportType = 'ytp' | 'whitelist-bypass' | 'auto';
export type TunnelMode = 'dc' | 'video';
export type Platform = 'vk' | 'telemost' | 'wbstream' | 'auto';
export type DeviceType = 'android' | 'ios' | 'linux' | 'windows' | 'mac' | 'other';
export type ClientStatus = 'online' | 'offline';
export type ServerStatus = 'online' | 'offline' | 'degraded';

export interface Server {
  readonly id: string;
  readonly name: string;
  readonly country: string;
  readonly countryCode: string;
  readonly city: string;
  readonly ip: string;
  readonly port: number;
  readonly status: ServerStatus;
  readonly transportType: TransportType;
  readonly maxClients: number;
  readonly currentLoad: number;
  readonly bandwidth: string;
  readonly ping: number;
  readonly uptime: number;
  readonly healthStatus: 'healthy' | 'degraded' | 'offline';
  readonly lastHealthCheck: string;
  readonly version: string;
  readonly createdAt: string;
  readonly updatedAt: string;
  readonly _count?: {
    readonly clients: number;
  };
}

export interface Client {
  readonly id: string;
  readonly name: string;
  readonly deviceType: DeviceType;
  readonly deviceName: string;
  readonly status: ClientStatus;
  readonly connectedServerId?: string;
  readonly connectedServer?: Server;
  readonly transportMode: TransportType;
  readonly tunnelMode: TunnelMode;
  readonly platform: Platform;
  readonly socksPort: number;
  readonly autoConnect: boolean;
  readonly preferredServerId?: string;
  readonly totalDataUsed: number;
  readonly sessionDuration: number;
  readonly bandwidthLimit: number;
  readonly bandwidthUsed: number;
  readonly downloadSpeed: number;
  readonly uploadSpeed: number;
  readonly lastSeen: string;
}

export interface ConnectionLog {
  readonly id: string;
  readonly clientId: string;
  readonly serverId: string | null;
  readonly event: string;
  readonly message: string;
  readonly timestamp: string;
  readonly createdAt: string;
  readonly server: Server | null;
  readonly duration: string | null;
}

interface ClientStoreState {
  servers: Server[];
  clients: Client[];
  logs: ConnectionLog[];
  runtimeNodes: RuntimeNode[];
  carrierHealth: Record<string, CarrierHealthSnapshot>;
  desktopStatus: DesktopRuntimeStatus | null;
  daemonLogs: string[];
  smokeTest: SmokeTestResult | null;
  activeTab?: string;
  fetchServers: () => Promise<void>;
  fetchClients: () => Promise<void>;
  fetchLogs: () => Promise<void>;
  connectClient: (clientId: string, serverId?: string) => Promise<void>;
  disconnectClient: (clientId: string) => Promise<void>;
  updateClient: (clientId: string, data: Partial<Client>) => Promise<void>;
  setActiveTab?: (tab: string) => void;
  refreshDesktopTelemetry: () => Promise<void>;
  runDesktopSmokeTest: (nodeId?: string) => Promise<SmokeTestResult>;
  restartRuntime: () => Promise<void>;
}

const CLIENT_ID = 'this-device';

const now = (): string => new Date().toISOString();

const seedServers: Server[] = [
  {
    id: 'srv-de-1',
    name: 'Frankfurt',
    country: 'Germany',
    countryCode: 'DE',
    city: 'Frankfurt',
    ip: '10.0.0.1',
    port: 443,
    status: 'online',
    transportType: 'auto',
    maxClients: 100,
    currentLoad: 34,
    bandwidth: '1 Gbps',
    ping: 24,
    uptime: 99.98,
    healthStatus: 'healthy',
    lastHealthCheck: now(),
    version: '1.0.0',
    createdAt: now(),
    updatedAt: now(),
    _count: { clients: 12 },
  },
  {
    id: 'srv-nl-1',
    name: 'Amsterdam',
    country: 'Netherlands',
    countryCode: 'NL',
    city: 'Amsterdam',
    ip: '10.0.0.2',
    port: 443,
    status: 'online',
    transportType: 'ytp',
    maxClients: 100,
    currentLoad: 58,
    bandwidth: '1 Gbps',
    ping: 31,
    uptime: 99.91,
    healthStatus: 'healthy',
    lastHealthCheck: now(),
    version: '1.0.0',
    createdAt: now(),
    updatedAt: now(),
    _count: { clients: 27 },
  },
  {
    id: 'srv-fi-1',
    name: 'Helsinki',
    country: 'Finland',
    countryCode: 'FI',
    city: 'Helsinki',
    ip: '10.0.0.3',
    port: 443,
    status: 'degraded',
    transportType: 'whitelist-bypass',
    maxClients: 80,
    currentLoad: 71,
    bandwidth: '500 Mbps',
    ping: 45,
    uptime: 98.4,
    healthStatus: 'degraded',
    lastHealthCheck: now(),
    version: '1.0.0',
    createdAt: now(),
    updatedAt: now(),
    _count: { clients: 9 },
  },
];

const seedClient: Client = {
  id: CLIENT_ID,
  name: 'My Device',
  deviceType: 'android',
  deviceName: 'Pixel',
  status: 'offline',
  transportMode: 'auto',
  tunnelMode: 'dc',
  platform: 'auto',
  socksPort: 1080,
  autoConnect: false,
  preferredServerId: undefined,
  totalDataUsed: 0,
  sessionDuration: 0,
  bandwidthLimit: 0,
  bandwidthUsed: 0,
  downloadSpeed: 0,
  uploadSpeed: 0,
  lastSeen: now(),
};

const pushLog = (logs: ConnectionLog[], clientId: string, event: string, message: string, server: Server | null = null): ConnectionLog[] => {
  const timestamp = now();
  return [
    {
      id: `log-${Date.now()}-${Math.random().toString(16).slice(2, 8)}`,
      clientId,
      serverId: server?.id ?? null,
      event,
      message,
      timestamp,
      createdAt: timestamp,
      server,
      duration: null,
    },
    ...logs,
  ].slice(0, 200);
};

const runtimeNodeToServer = (node: RuntimeNode, index: number): Server => {
  const timestamp = now();
  const healthy = node.reachable || Boolean(node.available);
  const latency = node.latency_ms ?? node.latencyMs ?? 0;
  return {
    id: node.node_id,
    name: node.label ?? (healthy ? `Node ${index + 1}` : `${node.node_id} (unreachable)`),
    country: node.country ?? 'Unknown',
    countryCode: node.country?.slice(0, 2).toUpperCase() || 'UN',
    city: node.region ?? 'Runtime',
    ip: '127.0.0.1',
    port: 0,
    status: healthy ? 'online' : 'offline',
    transportType: 'auto',
    maxClients: 1,
    currentLoad: 0,
    bandwidth: 'Unknown',
    ping: healthy ? latency || 32 : 0,
    uptime: 0,
    healthStatus: healthy ? 'healthy' : 'offline',
    lastHealthCheck: node.last_seen_at ?? timestamp,
    version: 'runtime',
    createdAt: timestamp,
    updatedAt: timestamp,
    _count: { clients: 0 },
  };
};

const initialServers: Server[] = isHosted() ? [] : seedServers;

export const useClientStore = create<ClientStoreState>((set, get) => ({
  servers: initialServers,
  clients: [seedClient],
  logs: [],
  runtimeNodes: [],
  carrierHealth: {},
  desktopStatus: null,
  daemonLogs: [],
  smokeTest: null,
  activeTab: 'client',
  async fetchServers(): Promise<void> {
    if (!isHosted()) {
      set({ servers: seedServers });
      return;
    }
    const nodes = await WtTransport.getRuntimeNodes();
    set({
      runtimeNodes: nodes,
      servers: nodes.map(runtimeNodeToServer),
    });
  },
  async fetchClients(): Promise<void> {
    set((state) => ({ clients: state.clients }));
  },
  async fetchLogs(): Promise<void> {
    if (!isDesktopHosted()) {
      return;
    }
    const daemonLogs = await WtTransport.getDaemonLogs();
    set({ daemonLogs });
  },
  async connectClient(clientId: string, serverId?: string): Promise<void> {
    const targetServerId = serverId || get().servers.find((candidate) => candidate.status !== 'offline')?.id;
    const server = targetServerId
      ? get().servers.find((candidate) => candidate.id === targetServerId) ?? null
      : null;
    const hosted = isHosted();
    let connected = true;
    let lifecycle = 'connected';
    if (hosted) {
      // Let errors propagate — the view catches and shows a toast
      await WtTransport.connect({ serverId: targetServerId });
      const [status, connectionState] = await Promise.all([
        WtTransport.getStatus(),
        WtTransport.getConnectionState(),
      ]);
      connected = status.active && connectionState.lifecycle === 'connected';
      lifecycle = connected ? 'connected' : connectionState.lifecycle;
    }
    set((state) => ({
      clients: state.clients.map((client) =>
        client.id === clientId
          ? {
              ...client,
              status: connected ? 'online' : 'offline',
              connectedServerId: connected ? targetServerId : undefined,
              connectedServer: connected ? server ?? undefined : undefined,
              preferredServerId: targetServerId,
              lastSeen: now(),
            }
          : client,
      ),
      logs: pushLog(
        state.logs,
        clientId,
        connected ? 'connect' : `connect-${lifecycle}`,
        connected
          ? `Connected via ${server?.name ?? targetServerId ?? 'auto'}`
          : `Connection ${lifecycle} via ${server?.name ?? targetServerId ?? 'auto'}`,
        server,
      ),
    }));
    await get().refreshDesktopTelemetry();
  },
  async disconnectClient(clientId: string): Promise<void> {
    if (isHosted()) {
      await WtTransport.disconnect();
    }
    set((state) => ({
      clients: state.clients.map((client) =>
        client.id === clientId
          ? {
              ...client,
              status: 'offline',
              connectedServerId: undefined,
              connectedServer: undefined,
              sessionDuration: 0,
              lastSeen: now(),
            }
          : client,
      ),
      logs: pushLog(state.logs, clientId, 'disconnect', 'Disconnected'),
    }));
    await get().refreshDesktopTelemetry();
  },
  async updateClient(clientId: string, data: Partial<Client>): Promise<void> {
    set((state) => ({
      clients: state.clients.map((client) => (client.id === clientId ? { ...client, ...data } : client)),
    }));
  },
  setActiveTab: (tab: string) => set({ activeTab: tab }),
  async refreshDesktopTelemetry(): Promise<void> {
    if (!isDesktopHosted()) {
      return;
    }
    const [desktopStatus, daemonLogs, carrierHealth] = await Promise.all([
      WtTransport.getDesktopStatus(),
      WtTransport.getDaemonLogs(),
      WtTransport.getCarrierHealth(),
    ]);
    set({ desktopStatus, daemonLogs, carrierHealth });
  },
  async runDesktopSmokeTest(nodeId?: string): Promise<SmokeTestResult> {
    const result = await WtTransport.runSmokeTest({ nodeId });
    set((state) => ({
      smokeTest: result,
      logs: pushLog(state.logs, CLIENT_ID, 'smoke-test', result.summary),
    }));
    return result;
  },
  async restartRuntime(): Promise<void> {
    await WtTransport.restartRuntime();
    await get().refreshDesktopTelemetry();
  },
}));

function syncClientStatus(status: string, active: boolean): void {
  useClientStore.setState((state) => ({
    clients: state.clients.map((client) =>
      client.id === CLIENT_ID
        ? {
            ...client,
            status: active ? 'online' : 'offline',
            lastSeen: now(),
          }
        : client,
    ),
    logs: pushLog(state.logs, CLIENT_ID, 'status', `runtime status: ${status}`),
  }));
}

let nativeBridgeInitPromise: Promise<void> | undefined;

async function initializeNativeBridge(): Promise<void> {

  await useClientStore.getState().fetchServers();

  WtTransport.addListener('statusChanged', ({ status, active }: { status: string; active: boolean }) => {
    syncClientStatus(status, active);
  }).catch(() => undefined);

  WtTransport.addListener('log', ({ message }: { message: string }) => {
    useClientStore.setState((state) => ({
      daemonLogs: [message, ...state.daemonLogs].slice(0, 500),
      logs: pushLog(state.logs, CLIENT_ID, 'log', message),
    }));
  }).catch(() => undefined);

  WtTransport.addListener('carrierHealth', (snapshot: Record<string, CarrierHealthSnapshot>) => {
    useClientStore.setState({ carrierHealth: snapshot });
  }).catch(() => undefined);

  // Android may recreate the Activity while the native runtime keeps running.
  // Restore the web store from the authoritative native state on every init.
  const initialStatus = await WtTransport.getStatus();
  syncClientStatus(initialStatus.status, initialStatus.active);

  try {
    const socks = await WtTransport.getSocksInfo();
    useClientStore.setState((state) => ({
      clients: state.clients.map((client) =>
        client.id === CLIENT_ID
          ? {
              ...client,
              socksPort: socks.port,
            }
          : client,
      ),
    }));
  } catch {
    // Leave the default port when the runtime is not yet available.
  }

  if (isDesktopHosted()) {
    await useClientStore.getState().refreshDesktopTelemetry();
  }
}

/** Register native listeners once per web runtime, including React StrictMode remounts. */
export function initNativeBridge(): Promise<void> {
  if (!isHosted()) return Promise.resolve();
  if (!nativeBridgeInitPromise) {
    nativeBridgeInitPromise = initializeNativeBridge().catch((error: unknown) => {
      nativeBridgeInitPromise = undefined;
      throw error;
    });
  }
  return nativeBridgeInitPromise;
}

export default useClientStore;
