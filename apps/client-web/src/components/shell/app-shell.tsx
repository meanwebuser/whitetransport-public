import { useCallback, useEffect, useMemo, useState } from 'react'

import WtTransport, { isHosted } from '../../native/wt-transport'
import type { ConnectionLifecycle, SmokeTestResult, SystemVPNState, WtCapabilities, WtConnectionState, WtLogInfo, WtRoutingSettings, WtStatus } from '../../native/transport-contract'
import { useClientStore, type Client, type Server } from '../../store/client-store'
import { BottomNavigation, type ShellTab } from './bottom-navigation'
import { EndpointsScreen } from './endpoints-screen'
import { HomeScreen } from './home-screen'
import { SettingsScreen } from './settings-screen'

interface NativeSnapshot {
  readonly capabilities: WtCapabilities | null
  readonly status: WtStatus | null
  readonly connectionState: WtConnectionState | null
}

export interface AppShellProps {
  readonly initialTab?: ShellTab
}

const initialSnapshot: NativeSnapshot = { capabilities: null, status: null, connectionState: null }

/** Shared presentation shell for Wails and Capacitor hosts. The native adapter remains the source of lifecycle truth. */
export function AppShell({ initialTab = 'home' }: AppShellProps) {
  const servers = useClientStore((state) => state.servers)
  const runtimeNodes = useClientStore((state) => state.runtimeNodes)
  const clients = useClientStore((state) => state.clients)
  const fetchServers = useClientStore((state) => state.fetchServers)
  const connectClient = useClientStore((state) => state.connectClient)
  const disconnectClient = useClientStore((state) => state.disconnectClient)
  const updateClient = useClientStore((state) => state.updateClient)

  const [activeTab, setActiveTab] = useState<ShellTab>(initialTab)
  const [snapshot, setSnapshot] = useState<NativeSnapshot>(initialSnapshot)
  const [splitRouting, setSplitRouting] = useState<WtRoutingSettings | null>(null)
  const [logs, setLogs] = useState<WtLogInfo | null>(null)
  const [smokeTestResult, setSmokeTestResult] = useState<SmokeTestResult | null>(null)
  const [logError, setLogError] = useState<string>()
  const [refreshing, setRefreshing] = useState(false)
  const [loadingSplitRouting, setLoadingSplitRouting] = useState(false)
  const [busy, setBusy] = useState(false)
  const [smokeTestBusy, setSmokeTestBusy] = useState(false)
  const [endpointId, setEndpointId] = useState<string>()
  const [manualError, setManualError] = useState<string>()

  const client: Client | undefined = clients.find((candidate) => candidate.id === 'this-device') ?? clients[0]
  const clientId = client?.id
  const defaultServer = useMemo(
    () => servers.find((server) => server.id === client?.preferredServerId) ?? servers.find((server) => server.status !== 'offline'),
    [client?.preferredServerId, servers],
  )
  const refreshNative = useCallback(async () => {
    try {
      const [capabilities, status, connectionState] = await Promise.all([
        WtTransport.getCapabilities(),
        WtTransport.getStatus(),
        WtTransport.getConnectionState(),
      ])
      setSnapshot({ capabilities, status, connectionState })
      setManualError(undefined)
    } catch (error) {
      setManualError(isBrowserUnsupportedError(error) ? undefined : error instanceof Error ? error.message : 'Native runtime недоступен')
    }
  }, [])

  useEffect(() => {
    void fetchServers().catch((error: unknown) => setManualError(error instanceof Error ? error.message : 'Не удалось обновить endpoints'))
    void refreshNative()
  }, [fetchServers, refreshNative])

  useEffect(() => {
    let disposed = false
    const refreshFromHost = () => {
      if (disposed) return
      void fetchServers().catch((error: unknown) => setManualError(error instanceof Error ? error.message : 'Не удалось обновить endpoints'))
      void refreshNative()
    }
    void WtTransport.addListener('statusChanged', refreshFromHost).catch(() => undefined)
    // Wails currently has no push event surface. Polling keeps OS-level tunnel
    // loss visible without pretending its unsupported listener succeeded.
    const timer = window.setInterval(refreshFromHost, 2_000)
    return () => {
      disposed = true
      window.clearInterval(timer)
    }
  }, [fetchServers, refreshNative])

  useEffect(() => {
    if (!endpointId && defaultServer?.id) setEndpointId(defaultServer.id)
  }, [defaultServer?.id, endpointId])

  const connectionState = snapshot.connectionState
  const status = snapshot.status
  const lifecycle = useMemo(() => resolveLifecycle(connectionState, status, manualError), [connectionState, manualError, status])
  const systemVpnReady = connectionState?.systemVpnState === 'connected' || status?.systemVpnState === 'connected'
  const connected = Boolean(status?.active && connectionState?.lifecycle === 'connected' && systemVpnReady)
  const sessionActive = Boolean(
    status?.active
    || systemVpnReady
    || connectionState?.transportState === 'connected'
    || connectionState?.lifecycle === 'connecting'
    || connectionState?.lifecycle === 'connected'
    || connectionState?.lifecycle === 'degraded'
    || connectionState?.lifecycle === 'disconnecting',
  )
  const authoritativeServerId = connectionState?.selectedNodeId ?? client?.connectedServerId
  const selectedServerId = (sessionActive ? authoritativeServerId : undefined) ?? endpointId ?? client?.preferredServerId ?? defaultServer?.id
  const selectedServer = servers.find((server) => server.id === selectedServerId) ?? defaultServer
  const browserUnsupported = !isHosted() || snapshot.capabilities?.host === 'browser'

  const selectEndpoint = useCallback(async (serverId: string) => {
    if (sessionActive) {
      setManualError('Сначала отключите VPN, чтобы сменить endpoint')
      return
    }
    setEndpointId(serverId)
    if (!clientId) {
      setManualError('Устройство не зарегистрировано')
      return
    }
    await updateClient(clientId, { preferredServerId: serverId })
  }, [clientId, sessionActive, updateClient])

  const handlePower = useCallback(async () => {
    if (busy) return
    setBusy(true)
    try {
      if (!clientId) {
        setManualError('Устройство не зарегистрировано')
        return
      }
      if (sessionActive) {
        await disconnectClient(clientId)
      } else {
        await connectClient(clientId, selectedServerId)
      }
      await refreshNative()
    } catch (error) {
      setManualError(error instanceof Error ? error.message : 'Операция подключения завершилась ошибкой')
      await refreshNative()
    } finally {
      setBusy(false)
    }
  }, [busy, clientId, connectClient, disconnectClient, refreshNative, selectedServerId, sessionActive])

  const handlePermission = useCallback(async () => {
    setBusy(true)
    try {
      await WtTransport.requestVPNPermission()
      await refreshNative()
    } catch (error) {
      setManualError(error instanceof Error ? error.message : 'Разрешение VPN недоступно')
    } finally {
      setBusy(false)
    }
  }, [refreshNative])

  const handleRefresh = useCallback(async () => {
    setRefreshing(true)
    try {
      await fetchServers()
      await refreshNative()
    } finally {
      setRefreshing(false)
    }
  }, [fetchServers, refreshNative])

  const loadSplitRouting = useCallback(async () => {
    setLoadingSplitRouting(true)
    try {
      setSplitRouting(await WtTransport.getSplitRouting())
    } catch (error) {
      setManualError(error instanceof Error ? error.message : 'Настройки split routing недоступны')
    } finally {
      setLoadingSplitRouting(false)
    }
  }, [])

  const saveSplitRouting = useCallback(async (settings: WtRoutingSettings) => {
    setLoadingSplitRouting(true)
    try {
      setSplitRouting(await WtTransport.setSplitRouting(settings))
    } catch (error) {
      setManualError(error instanceof Error ? error.message : 'Не удалось сохранить split routing')
    } finally {
      setLoadingSplitRouting(false)
    }
  }, [])

  const loadLogs = useCallback(async () => {
    setLogError(undefined)
    try {
      setLogs(await WtTransport.getLogInfo())
    } catch (error) {
      setLogError(error instanceof Error ? error.message : 'Логи недоступны')
    }
  }, [])

  const runSmokeTest = useCallback(async () => {
    setSmokeTestBusy(true)
    try {
      setSmokeTestResult(await WtTransport.runSmokeTest({ nodeId: selectedServerId }))
    } catch (error) {
      setManualError(error instanceof Error ? error.message : 'Тестовый режим недоступен')
    } finally {
      setSmokeTestBusy(false)
    }
  }, [selectedServerId])

  return (
    <div className="shell-app" data-testid="app-shell">
      <main className="shell-main">
        {activeTab === 'home' ? (
          <HomeScreen
            client={client}
            servers={servers}
            selectedServer={selectedServer}
            selectedServerId={selectedServerId}
            clientAvailable={Boolean(clientId)}
            lifecycle={lifecycle}
            sessionActive={sessionActive}
            systemVpnState={connectionState?.systemVpnState ?? status?.systemVpnState ?? 'unsupported'}
            transportState={connectionState?.transportState ?? status?.transportState ?? 'disconnected'}
            latency={selectedServer?.ping}
            error={manualError}
            browserUnsupported={browserUnsupported}
            busy={busy}
            onPower={handlePower}
            onPermission={handlePermission}
            onEndpointChange={selectEndpoint}
            onOpenEndpoints={() => setActiveTab('endpoints')}
          />
        ) : activeTab === 'endpoints' ? (
          <EndpointsScreen
            servers={servers}
            runtimeNodes={runtimeNodes}
            selectedServerId={selectedServerId}
            selectionDisabled={sessionActive}
            refreshing={refreshing}
            onRefresh={handleRefresh}
            onSelect={selectEndpoint}
          />
        ) : (
          <SettingsScreen
            capabilities={snapshot.capabilities}
            splitRouting={splitRouting}
            logs={logs}
            logError={logError}
            actionError={manualError}
            loadingSplitRouting={loadingSplitRouting}
            onLoadSplitRouting={loadSplitRouting}
            onSetSplitRouting={saveSplitRouting}
            onLoadLogs={loadLogs}
            smokeTestResult={smokeTestResult}
            smokeTestBusy={smokeTestBusy}
            onRunSmokeTest={runSmokeTest}
          />
        )}
      </main>
      <BottomNavigation activeTab={activeTab} onChange={setActiveTab} />
    </div>
  )
}

function isBrowserUnsupportedError(error: unknown): boolean {
  if (isHosted() || typeof error !== 'object' || error === null) return false
  return (error as { readonly code?: unknown }).code === 'UNSUPPORTED_CAPABILITY'
}

function resolveLifecycle(connectionState: WtConnectionState | null, status: WtStatus | null, error?: string): ConnectionLifecycle {
  if (error && !connectionState) return 'error'
  if (connectionState?.lifecycle === 'permission_required' || connectionState?.systemVpnState === 'permission_required') return 'permission_required'
  // Native transition and failure states are authoritative even while the
  // transport drains and still reports connected. Only a claimed connected
  // lifecycle is downgraded when the OS VPN proof is incomplete.
  if (connectionState?.lifecycle === 'connecting'
    || connectionState?.lifecycle === 'disconnecting'
    || connectionState?.lifecycle === 'degraded'
    || connectionState?.lifecycle === 'unsupported'
    || connectionState?.lifecycle === 'error') return connectionState.lifecycle
  const systemVpnReady = connectionState?.systemVpnState === 'connected' || status?.systemVpnState === 'connected'
  if (connectionState?.lifecycle === 'connected' && status?.active && systemVpnReady) return 'connected'
  if (connectionState?.lifecycle === 'connected' || status?.transportState === 'connected') return 'degraded'
  return connectionState?.lifecycle ?? 'disconnected'
}

export type { ShellTab }
