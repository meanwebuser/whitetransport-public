// @vitest-environment jsdom

import '@testing-library/jest-dom/vitest'
import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const fixtures = vi.hoisted(() => {
  const servers = [
    {
      id: 'node-berlin',
      name: 'Berlin',
      country: 'Germany',
      countryCode: 'DE',
      city: 'Berlin',
      ip: '127.0.0.1',
      port: 443,
      status: 'online',
      transportType: 'auto',
      maxClients: 1,
      currentLoad: 0,
      bandwidth: '1 Gbps',
      ping: 42,
      uptime: 100,
      healthStatus: 'healthy',
      lastHealthCheck: '2026-07-20T00:00:00Z',
      version: 'runtime',
      createdAt: '2026-07-20T00:00:00Z',
      updatedAt: '2026-07-20T00:00:00Z',
      _count: { clients: 0 },
    },
    {
      id: 'node-helsinki',
      name: 'Helsinki',
      country: 'Finland',
      countryCode: 'FI',
      city: 'Helsinki',
      ip: '127.0.0.1',
      port: 443,
      status: 'degraded',
      transportType: 'auto',
      maxClients: 1,
      currentLoad: 0,
      bandwidth: '500 Mbps',
      ping: 118,
      uptime: 99,
      healthStatus: 'degraded',
      lastHealthCheck: '2026-07-20T00:00:00Z',
      version: 'runtime',
      createdAt: '2026-07-20T00:00:00Z',
      updatedAt: '2026-07-20T00:00:00Z',
      _count: { clients: 0 },
    },
  ]
  const clientTemplate = {
    id: 'this-device',
    name: 'This device',
    deviceType: 'desktop',
    deviceName: 'Test device',
    status: 'offline',
    connectedServerId: undefined,
    connectedServer: undefined,
    preferredServerId: undefined as string | undefined,
    transportMode: 'auto',
    tunnelMode: 'dc',
    platform: 'auto',
    socksPort: 1080,
    autoConnect: false,
    totalDataUsed: 0,
    sessionDuration: 0,
    bandwidthLimit: 0,
    bandwidthUsed: 0,
    downloadSpeed: 0,
    uploadSpeed: 0,
    lastSeen: '2026-07-20T00:00:00Z',
  }
  const clients = [{ ...clientTemplate }]
  const state = {
    servers,
    clients,
    logs: [],
    runtimeNodes: [],
    carrierHealth: {},
    desktopStatus: null,
    daemonLogs: [],
    smokeTest: null,
    fetchServers: vi.fn().mockResolvedValue(undefined),
    fetchClients: vi.fn().mockResolvedValue(undefined),
    fetchLogs: vi.fn().mockResolvedValue(undefined),
    connectClient: vi.fn().mockResolvedValue(undefined),
    disconnectClient: vi.fn().mockResolvedValue(undefined),
    updateClient: vi.fn().mockResolvedValue(undefined),
    refreshDesktopTelemetry: vi.fn().mockResolvedValue(undefined),
    runDesktopSmokeTest: vi.fn().mockResolvedValue(undefined),
    restartRuntime: vi.fn().mockResolvedValue(undefined),
  }
  return { servers, clients, clientTemplate, state }
})

const native = vi.hoisted(() => ({
  statusListeners: [] as Array<(status: unknown) => void>,
  capabilities: {
    host: 'wails',
    transport: true,
    endpoints: true,
    logs: true,
    splitRouting: true,
    proxyRouting: true,
    systemVpn: true,
    requestVpnPermission: true,
    startSystemVpn: true,
    stopSystemVpn: true,
    smokeTest: true,
  },
  status: {
    state: 'disconnected',
    status: 'disconnected',
    active: false,
    mode: 'off',
    transportState: 'disconnected',
    systemVpnState: 'disconnected',
  },
  connectionState: {
    lifecycle: 'disconnected',
    transportState: 'disconnected',
    systemVpnState: 'disconnected',
  },
  transport: {
    getCapabilities: vi.fn(),
    getStatus: vi.fn(),
    getConnectionState: vi.fn(),
    getSplitRouting: vi.fn().mockResolvedValue({ mode: 'none', lan_access: false }),
    setSplitRouting: vi.fn().mockImplementation(async (settings) => settings),
    getLogInfo: vi.fn().mockResolvedValue({ path: '/var/log/whitetransport.jsonl', lines: ['ready'], persistent: true }),
    getRoomAuthStatus: vi.fn().mockResolvedValue(false),
    runSmokeTest: vi.fn().mockResolvedValue({ passed: true, totalDurationMs: 12, summary: 'GUI test mode passed', steps: [], logPath: '/tmp/WhiteTransport.log', resultPath: '/tmp/whitetransport-test-result.json' }),
    requestVPNPermission: vi.fn().mockResolvedValue(undefined),
    startSystemVPN: vi.fn().mockResolvedValue(undefined),
    stopSystemVPN: vi.fn().mockResolvedValue(undefined),
    addListener: vi.fn().mockResolvedValue(undefined),
  },
  isHosted: vi.fn().mockReturnValue(true),
  isDesktopHosted: vi.fn().mockReturnValue(true),
}))

vi.mock('../../store/client-store', () => ({
  useClientStore: (selector?: (state: typeof fixtures.state) => unknown) =>
    selector ? selector(fixtures.state) : fixtures.state,
}))

vi.mock('../../native/wt-transport', () => ({
  default: native.transport,
  isHosted: native.isHosted,
  isDesktopHosted: native.isDesktopHosted,
}))

import { AppShell } from './app-shell'
import { formatNodeCount } from './endpoints-screen'
import { normalizeDestinationIds } from './settings-screen'

function renderShell() {
  return render(<AppShell />)
}

describe('shared native client shell', () => {
  afterEach(() => cleanup())

  beforeEach(() => {
    vi.clearAllMocks()
    native.capabilities.host = 'wails'
    native.transport.getCapabilities.mockResolvedValue(native.capabilities)
    native.transport.getStatus.mockResolvedValue(native.status)
    native.transport.getConnectionState.mockResolvedValue(native.connectionState)
    native.transport.getSplitRouting.mockResolvedValue({ mode: 'none', lan_access: false })
    native.transport.getLogInfo.mockResolvedValue({ path: '/var/log/whitetransport.jsonl', lines: ['ready'], persistent: true })
    native.statusListeners.splice(0)
    native.transport.addListener.mockImplementation(async (eventName: string, listener: (status: unknown) => void) => {
      if (eventName === 'statusChanged') native.statusListeners.push(listener)
    })
    native.isHosted.mockReturnValue(true)
    native.isDesktopHosted.mockReturnValue(true)
    fixtures.state.clients.splice(0, fixtures.state.clients.length, { ...fixtures.clientTemplate })
    fixtures.state.clients[0].status = 'offline'
    fixtures.state.clients[0].connectedServerId = undefined
    fixtures.state.clients[0].connectedServer = undefined
  })

  it('renders three accessible tabs and places the selected endpoint under the power control', async () => {
    renderShell()

    expect(await screen.findByTestId('app-shell')).toBeInTheDocument()
    expect(screen.getByTestId('power-control')).toBeInTheDocument()
    expect(screen.getByTestId('selected-endpoint')).toBeInTheDocument()
    const power = screen.getByTestId('power-control')
    const endpoint = screen.getByTestId('selected-endpoint')
    expect(power.compareDocumentPosition(endpoint) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()

    expect(screen.getByTestId('nav-home')).toHaveAttribute('aria-current', 'page')
    expect(screen.getByTestId('nav-endpoints')).toHaveAccessibleName(/endpoints|конфигурации/i)
    expect(screen.getByTestId('nav-settings')).toHaveAccessibleName(/settings|настройки/i)
  })

  it('exposes an actionable reproducible test mode with known artifact paths', async () => {
    const user = userEvent.setup()
    renderShell()

    await user.click(screen.getByTestId('nav-settings'))
    expect(screen.getByTestId('test-mode-run')).toBeEnabled()
    await user.click(screen.getByTestId('test-mode-run'))
    expect(native.transport.runSmokeTest).toHaveBeenCalledWith({ nodeId: 'node-berlin' })
    expect(await screen.findByTestId('test-mode-result')).toHaveTextContent(/whitetransport-test-result\.json/)
  })

  it('connects and disconnects through the existing store while requiring authoritative connected state', async () => {
    const user = userEvent.setup()
    fixtures.state.connectClient.mockImplementationOnce(async () => {
      native.transport.getStatus.mockResolvedValue({ ...native.status, status: 'connected', state: 'connected', active: true })
      native.transport.getConnectionState.mockResolvedValue({
        ...native.connectionState,
        lifecycle: 'connected',
        transportState: 'connected',
        systemVpnState: 'connected',
      })
    })
    renderShell()

    await user.click(screen.getByTestId('power-control'))
    expect(fixtures.state.connectClient).toHaveBeenCalledWith('this-device', 'node-berlin')
    expect(await screen.findByTestId('connection-status')).toHaveTextContent(/подключено|connected/i)
    await user.click(screen.getByTestId('power-control'))
    expect(fixtures.state.disconnectClient).toHaveBeenCalledWith('this-device')
  })

  it('shows degraded instead of full connected when transport reports an incomplete VPN lifecycle', async () => {
    native.transport.getStatus.mockResolvedValue({ ...native.status, status: 'connected', state: 'connected', active: false })
    native.transport.getConnectionState.mockResolvedValue({
      ...native.connectionState,
      lifecycle: 'connected',
      transportState: 'connected',
      systemVpnState: 'unsupported',
    })

    renderShell()

    expect(await screen.findByTestId('connection-status')).toHaveTextContent(/нестаб|degrad/i)
    expect(screen.getByTestId('connection-status')).not.toHaveTextContent(/^подключено$/i)
  })

  it('preserves authoritative disconnecting while the transport is still connected', async () => {
    native.transport.getStatus.mockResolvedValue({
      ...native.status,
      status: 'disconnecting',
      state: 'disconnecting',
      active: false,
      transportState: 'connected',
      systemVpnState: 'disconnecting',
    })
    native.transport.getConnectionState.mockResolvedValue({
      ...native.connectionState,
      lifecycle: 'disconnecting',
      transportState: 'connected',
      systemVpnState: 'disconnecting',
      selectedNodeId: 'node-berlin',
    })

    renderShell()

    expect(await screen.findByTestId('connection-status')).toHaveTextContent(/отключение|disconnecting/i)
    expect(screen.getByTestId('power-control')).toHaveAccessibleName(/отключение|disconnecting/i)
  })

  it('labels an in-flight disconnect as disconnecting instead of connecting', async () => {
    const user = userEvent.setup()
    let finishDisconnect: (() => void) | undefined
    const disconnectFinished = new Promise<void>((resolve) => { finishDisconnect = resolve })
    native.transport.getStatus.mockResolvedValue({ ...native.status, status: 'connected', state: 'connected', active: true, transportState: 'connected', systemVpnState: 'connected' })
    native.transport.getConnectionState.mockResolvedValue({
      ...native.connectionState,
      lifecycle: 'connected',
      transportState: 'connected',
      systemVpnState: 'connected',
      selectedNodeId: 'node-berlin',
    })
    fixtures.state.disconnectClient.mockImplementationOnce(async () => {
      native.transport.getStatus.mockResolvedValue({ ...native.status, status: 'disconnecting', state: 'disconnecting', active: false, transportState: 'connected', systemVpnState: 'disconnecting' })
      native.transport.getConnectionState.mockResolvedValue({
        ...native.connectionState,
        lifecycle: 'disconnecting',
        transportState: 'connected',
        systemVpnState: 'disconnecting',
        selectedNodeId: 'node-berlin',
      })
      act(() => native.statusListeners[0]?.({ status: 'disconnecting', active: false }))
      await disconnectFinished
    })
    renderShell()
    await waitFor(() => expect(native.statusListeners).toHaveLength(1))

    const click = user.click(screen.getByTestId('power-control'))
    await waitFor(() => expect(screen.getByTestId('power-control')).toHaveAccessibleName(/отключение|disconnecting/i))
    finishDisconnect?.()
    await click
  })

  it('refreshes the visible lifecycle when the native host emits a status change', async () => {
    renderShell()
    expect(await screen.findByTestId('connection-status')).toHaveTextContent(/отключено|disconnected/i)
    await waitFor(() => expect(native.statusListeners).toHaveLength(1))

    native.transport.getStatus.mockResolvedValue({ ...native.status, status: 'connected', state: 'connected', active: true, systemVpnState: 'connected' })
    native.transport.getConnectionState.mockResolvedValue({
      ...native.connectionState,
      lifecycle: 'connected',
      transportState: 'connected',
      systemVpnState: 'connected',
      selectedNodeId: 'node-berlin',
    })
    act(() => native.statusListeners[0]({ status: 'connected', active: true }))

    expect(await screen.findByTestId('connection-status')).toHaveTextContent(/подключено|connected/i)
  })

  it('disconnects an active partial session instead of retrying connect', async () => {
    const user = userEvent.setup()
    native.transport.getStatus.mockResolvedValue({ ...native.status, status: 'degraded', state: 'degraded', active: true })
    native.transport.getConnectionState.mockResolvedValue({
      ...native.connectionState,
      lifecycle: 'degraded',
      transportState: 'connected',
      systemVpnState: 'error',
      selectedNodeId: 'node-berlin',
    })
    renderShell()

    const power = await screen.findByTestId('power-control')
    expect(power).toHaveAccessibleName(/отключить|disconnect/i)
    await user.click(power)

    expect(fixtures.state.disconnectClient).toHaveBeenCalledWith('this-device')
    expect(fixtures.state.connectClient).not.toHaveBeenCalled()
  })

  it('keeps the authoritative active endpoint visible and disables switching during a session', async () => {
    native.transport.getStatus.mockResolvedValue({ ...native.status, status: 'connected', state: 'connected', active: true, systemVpnState: 'connected' })
    native.transport.getConnectionState.mockResolvedValue({
      ...native.connectionState,
      lifecycle: 'connected',
      transportState: 'connected',
      systemVpnState: 'connected',
      selectedNodeId: 'node-berlin',
    })
    fixtures.state.clients[0].preferredServerId = 'node-helsinki'
    renderShell()

    expect(await screen.findByTestId('selected-endpoint-select')).toHaveValue('node-berlin')
    expect(screen.getByTestId('selected-endpoint-select')).toBeDisabled()
  })

  it('surfaces permission and browser-unsupported states without hiding the setup action', async () => {
    native.transport.getConnectionState.mockResolvedValue({
      ...native.connectionState,
      lifecycle: 'permission_required',
      systemVpnState: 'permission_required',
    })
    const view = renderShell()
    expect(await screen.findByTestId('connection-status')).toHaveTextContent(/разреш|permission/i)
    expect(screen.getByRole('button', { name: /разреш|permission/i })).toBeInTheDocument()

    native.capabilities.host = 'browser'
    native.transport.getCapabilities.mockResolvedValue({ ...native.capabilities, host: 'browser', transport: false, systemVpn: false })
    native.isHosted.mockReturnValue(false)
    native.transport.getStatus.mockRejectedValue(Object.assign(new Error('transport is unsupported on browser'), { code: 'UNSUPPORTED_CAPABILITY' }))
    view.rerender(<AppShell />)
    expect(await screen.findByTestId('browser-unsupported')).toBeInTheDocument()
    expect(screen.getByText(/браузер|browser/i)).toBeInTheDocument()
    expect(screen.queryByText('transport is unsupported on browser')).not.toBeInTheDocument()
  })

  it('keeps real native errors visible while suppressing only browser unsupported noise', async () => {
    native.capabilities.host = 'wails'
    native.transport.getCapabilities.mockResolvedValue({ ...native.capabilities, host: 'wails' })
    native.transport.getStatus.mockRejectedValue(new Error('native runtime exploded'))
    native.transport.getConnectionState.mockRejectedValue(new Error('native runtime exploded'))

    renderShell()

    expect(await screen.findByText('native runtime exploded')).toBeInTheDocument()
    expect(screen.queryByTestId('browser-unsupported')).not.toBeInTheDocument()
  })

  it('switches tabs, refreshes discovered endpoints, and lets the operator choose a node', async () => {
    const user = userEvent.setup()
    renderShell()
    await user.click(screen.getByTestId('nav-endpoints'))

    expect(screen.getByRole('heading', { name: /endpoints|конфигурации/i })).toBeInTheDocument()
    expect(screen.getByTestId('endpoint-node-berlin')).toBeInTheDocument()
    expect(screen.getByTestId('endpoint-node-helsinki')).toHaveTextContent(/118\s*ms/)

    await user.click(screen.getByRole('button', { name: /refresh|обновить/i }))
    expect(fixtures.state.fetchServers).toHaveBeenCalled()

    await user.click(screen.getByTestId('endpoint-select-node-helsinki'))
    expect(fixtures.state.updateClient).toHaveBeenCalledWith('this-device', { preferredServerId: 'node-helsinki' })
    expect(screen.queryByTestId('manual-endpoint-input')).not.toBeInTheDocument()
  })

  it('refreshes endpoints when the native status poll runs', async () => {
    vi.useFakeTimers()

    renderShell()
    await act(async () => undefined)
    expect(fixtures.state.fetchServers).toHaveBeenCalledTimes(1)
    await act(async () => {
      vi.advanceTimersByTime(2_000)
      await Promise.resolve()
    })

    expect(fixtures.state.fetchServers).toHaveBeenCalledTimes(2)
    cleanup()
    vi.useRealTimers()
  })

  it('gates split routing controls on capabilities and keeps diagnostics out of Home', async () => {
    const user = userEvent.setup()
    renderShell()
    expect(screen.queryByTestId('split-routing-section')).not.toBeInTheDocument()
    expect(screen.queryByTestId('log-lines')).not.toBeInTheDocument()

    await user.click(screen.getByTestId('nav-settings'))
    expect(await screen.findByTestId('split-routing-section')).toBeInTheDocument()
    expect(screen.getByTestId('split-destination-settings')).toBeInTheDocument()
    expect(screen.queryByTestId('split-package-settings')).not.toBeInTheDocument()
    expect(screen.getByTestId('diagnostics-details')).not.toHaveAttribute('open')
    await user.click(screen.getByTestId('diagnostics-summary'))
    expect(screen.getByTestId('diagnostics-details')).toHaveAttribute('open', '')

    await user.selectOptions(screen.getByTestId('split-routing-mode'), 'bypass')
    expect(native.transport.setSplitRouting).toHaveBeenCalledWith(expect.objectContaining({ mode: 'bypass' }))
  })

  it('hydrates and applies normalized CIDR destinations on Wails while preserving mode and LAN access', async () => {
    const user = userEvent.setup()
    native.transport.getSplitRouting.mockResolvedValue({ mode: 'bypass', lan_access: true, destinations: ['198.51.100.0/24'] })
    renderShell()
    await user.click(screen.getByTestId('nav-settings'))

    const destinations = await screen.findByTestId('split-destination-settings')
    const textarea = destinations.querySelector('textarea')
    expect(textarea).toHaveValue('198.51.100.0/24')
    expect(normalizeDestinationIds(' 198.51.100.0/24\n\n2001:db8::/32\n198.51.100.0/24 ')).toEqual(['198.51.100.0/24', '2001:db8::/32'])

    await user.clear(textarea!)
    await user.type(textarea!, ' 198.51.100.0/24\n\n2001:db8::/32\n198.51.100.0/24 ')
    await user.click(screen.getByTestId('split-routing-apply'))

    expect(native.transport.setSplitRouting).toHaveBeenCalledWith({
      mode: 'bypass',
      lan_access: true,
      destinations: ['198.51.100.0/24', '2001:db8::/32'],
    })
  })

  it('keeps Android split routing on package identifiers', async () => {
    const user = userEvent.setup()
    native.capabilities.host = 'capacitor'
    native.transport.getSplitRouting.mockResolvedValue({ mode: 'only', lan_access: false, packages: ['com.example.keep'] })
    renderShell()
    await user.click(screen.getByTestId('nav-settings'))

    expect(await screen.findByTestId('split-package-settings')).toBeInTheDocument()
    expect(screen.queryByTestId('split-destination-settings')).not.toBeInTheDocument()
    expect(screen.queryByText('Разрешить доступ к локальной сети')).not.toBeInTheDocument()
    const textarea = screen.getByTestId('split-package-settings').querySelector('textarea')
    expect(textarea).toHaveValue('com.example.keep')
    await user.clear(textarea!)
    await user.type(textarea!, ' com.example.new\ncom.example.keep\ncom.example.new ')
    await user.click(screen.getByTestId('split-routing-apply'))

    expect(native.transport.setSplitRouting).toHaveBeenCalledWith({
      mode: 'only',
      lan_access: false,
      packages: ['com.example.new', 'com.example.keep'],
    })
  })

  it('uses the native split mode vocabulary and surfaces routing failures on Settings', async () => {
    const user = userEvent.setup()
    native.transport.setSplitRouting.mockRejectedValueOnce(new Error('native routing rejected'))
    renderShell()
    await user.click(screen.getByTestId('nav-settings'))

    const mode = await screen.findByTestId('split-routing-mode')
    expect([...mode.querySelectorAll('option')].map((option) => option.value)).toEqual(['none', 'bypass', 'only'])
    await user.click(screen.getByTestId('split-routing-apply'))
    expect(await screen.findByRole('alert')).toHaveTextContent('native routing rejected')
  })

  it('reveals, copies, and exports known logs only when requested', async () => {
    const user = userEvent.setup()
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: vi.fn().mockResolvedValue(undefined) },
    })
    renderShell()
    await user.click(screen.getByTestId('nav-settings'))

    expect(screen.queryByTestId('log-lines')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /показать логи|show logs/i }))
    expect(await screen.findByTestId('log-lines')).toHaveTextContent('ready')
    await user.click(screen.getByRole('button', { name: /копировать|copy/i }))
    expect(navigator.clipboard.writeText).toHaveBeenCalledWith(expect.stringContaining('ready'))
    expect(screen.getByRole('link', { name: /экспорт|export/i })).toHaveAttribute('download')
  })

  it('keeps the power control keyboard reachable and exposes pressed state', async () => {
    const user = userEvent.setup()
    renderShell()
    const power = screen.getByTestId('power-control')
    expect(power).toHaveAttribute('aria-pressed', 'false')
    await user.tab()
    await waitFor(() => expect(document.activeElement).toBe(power))
  })

  it('uses the actual selected client id for endpoint updates and connection actions', async () => {
    const user = userEvent.setup()
    fixtures.state.clients[0].id = 'client-42'
    renderShell()

    await user.click(screen.getByTestId('selected-endpoint-select'))
    await user.selectOptions(screen.getByTestId('selected-endpoint-select'), 'node-helsinki')
    expect(fixtures.state.updateClient).toHaveBeenCalledWith('client-42', { preferredServerId: 'node-helsinki' })

    await user.click(screen.getByTestId('power-control'))
    expect(fixtures.state.connectClient).toHaveBeenCalledWith('client-42', 'node-helsinki')
  })

  it('reports an unavailable client instead of fabricating a device id', async () => {
    fixtures.state.clients.splice(0)
    renderShell()

    expect(await screen.findByTestId('client-unavailable')).toBeInTheDocument()
    expect(screen.getByTestId('power-control')).toBeDisabled()
    expect(fixtures.state.connectClient).not.toHaveBeenCalled()
  })

  it('uses the product status VPN state when the connection snapshot omits it', async () => {
    native.transport.getStatus.mockResolvedValue({ ...native.status, active: true, status: 'connected', state: 'connected', systemVpnState: 'connected' })
    native.transport.getConnectionState.mockResolvedValue({
      ...native.connectionState,
      lifecycle: 'connected',
      transportState: 'connected',
      systemVpnState: undefined,
    })
    renderShell()

    expect(await screen.findByTestId('connection-status')).toHaveTextContent(/подключено|connected/i)
    expect(screen.getByTestId('diagnostic-summary')).toHaveTextContent('Активен')
  })

  it('uses correct Russian endpoint count forms', () => {
    expect(formatNodeCount(1)).toBe('1 узел')
    expect(formatNodeCount(2)).toBe('2 узла')
    expect(formatNodeCount(5)).toBe('5 узлов')
    expect(formatNodeCount(21)).toBe('21 узел')
  })
})
