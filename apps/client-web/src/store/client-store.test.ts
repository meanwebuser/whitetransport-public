import { beforeEach, describe, expect, it, vi } from 'vitest';

const native = vi.hoisted(() => ({
  isHosted: vi.fn().mockReturnValue(false),
  isDesktopHosted: vi.fn().mockReturnValue(false),
  transport: {
    connect: vi.fn().mockResolvedValue(undefined),
    disconnect: vi.fn().mockResolvedValue(undefined),
    getStatus: vi.fn(),
    getConnectionState: vi.fn(),
    getRuntimeNodes: vi.fn().mockResolvedValue([]),
    getSocksInfo: vi.fn().mockResolvedValue({ host: '127.0.0.1', port: 1080 }),
    addListener: vi.fn().mockResolvedValue(undefined),
  },
}));

vi.mock('../native/wt-transport', () => ({
  default: native.transport,
  isHosted: native.isHosted,
  isDesktopHosted: native.isDesktopHosted,
}));

import { initNativeBridge, useClientStore } from './client-store';

describe('client connection state', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    native.isHosted.mockReturnValue(false);
    native.isDesktopHosted.mockReturnValue(false);
    useClientStore.setState((state) => ({
      clients: state.clients.map((client) => ({
        ...client,
        status: 'offline',
        connectedServerId: undefined,
        connectedServer: undefined,
      })),
      logs: [],
    }));
  });

  it('keeps a transport-only hosted connection offline when system VPN is unsupported', async () => {
    native.isHosted.mockReturnValue(true);
    native.transport.getStatus.mockResolvedValue({
      state: 'connected',
      status: 'connected',
      active: false,
      mode: 'proxy',
      transportState: 'connected',
      systemVpnState: 'unsupported',
    });
    native.transport.getConnectionState.mockResolvedValue({
      lifecycle: 'degraded',
      transportState: 'connected',
      systemVpnState: 'unsupported',
    });

    await useClientStore.getState().connectClient('this-device', 'srv-de-1');

    expect(native.transport.connect).toHaveBeenCalledWith({ serverId: 'srv-de-1' });
    expect(native.transport.getStatus).toHaveBeenCalledOnce();
    expect(native.transport.getConnectionState).toHaveBeenCalledOnce();
    expect(useClientStore.getState().clients.find((client) => client.id === 'this-device')).toMatchObject({
      status: 'offline',
      connectedServerId: undefined,
    });
    expect(useClientStore.getState().logs[0]).toMatchObject({ event: 'connect-degraded' });
  });

  it('initializes native listeners only once across repeated app mounts', async () => {
    native.isHosted.mockReturnValue(true);
    native.transport.getStatus.mockResolvedValue({
      state: 'disconnected',
      status: 'disconnected',
      active: false,
      mode: 'off',
      transportState: 'disconnected',
      systemVpnState: 'disconnected',
    });

    await Promise.all([initNativeBridge(), initNativeBridge()]);

    expect(native.transport.addListener).toHaveBeenCalledTimes(3);
  });
});
