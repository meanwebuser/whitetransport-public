import { useCallback, useEffect, useMemo, useRef, useState } from 'react'

import WtTransport, { isHosted } from '../../native/wt-transport'
import { getWailsApp, hasWailsHost } from '../../native/wails-transport'
import type { ConnectionLifecycle, SmokeTestResult, SystemVPNState, WtCapabilities, WtConnectionState, WtLogInfo, WtRoutingSettings, WtStatus } from '../../native/transport-contract'
import { useClientStore, type Client, type Server } from '../../store/client-store'
import { BottomNavigation, type ShellTab } from './bottom-navigation'
import { EndpointsScreen } from './endpoints-screen'
import { HomeScreen } from './home-screen'
import { carrierName, connectionFailureMessage, lifecycleLabels, type SessionEvent } from './session-diagnostics'
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

type TrafficCheck = { readonly sessionId?: string; readonly state: 'running' | 'passed' | 'failed'; readonly ip?: string }

/** Shared presentation shell for Wails and Capacitor hosts. The native adapter remains the source of lifecycle truth. */
export function AppShell({ initialTab = 'home' }: AppShellProps) {
  const servers = useClientStore((state) => state.servers)
  const runtimeNodes = useClientStore((state) => state.runtimeNodes)
  const clients = useClientStore((state) => state.clients)
  const fetchServers = useClientStore((state) => state.fetchServers)
  const connectClient = useClientStore((state) => state.connectClient)
  const disconnectClient = useClientStore((state) => state.disconnectClient)
  const updateClient = useClientStore((state) => state.updateClient)
  const refreshSocksInfo = useClientStore((state) => state.refreshSocksInfo)

  const [activeTab, setActiveTab] = useState<ShellTab>(initialTab)
  const [snapshot, setSnapshot] = useState<NativeSnapshot>(initialSnapshot)
  const [splitRouting, setSplitRouting] = useState<WtRoutingSettings | null>(null)
  const [logs, setLogs] = useState<WtLogInfo | null>(null)
  const [smokeTestResult, setSmokeTestResult] = useState<SmokeTestResult | null>(null)
  const [logError, setLogError] = useState<string>()
  const [refreshing, setRefreshing] = useState(false)
  const [loadingSplitRouting, setLoadingSplitRouting] = useState(false)
  const [busy, setBusy] = useState<'connect' | 'disconnect' | 'permission'>()
  const [smokeTestBusy, setSmokeTestBusy] = useState(false)
  const [endpointId, setEndpointId] = useState<string>()
  const [preferredTransport, setPreferredTransport] = useState('')
  const [manualError, setManualError] = useState<string>()
  const [refreshError, setRefreshError] = useState<string>()
  const [trafficCheck, setTrafficCheck] = useState<TrafficCheck>()
  const [changingTransport, setChangingTransport] = useState(false)
  const [sessionEvents, setSessionEvents] = useState<SessionEvent[]>([])
  const refreshRevision = useRef(0)

  const client: Client | undefined = clients.find((candidate) => candidate.id === 'this-device') ?? clients[0]
  const clientId = client?.id
  const defaultServer = useMemo(
    () => servers.find((server) => server.id === client?.preferredServerId) ?? servers.find((server) => server.status !== 'offline'),
    [client?.preferredServerId, servers],
  )
  const refreshNative = useCallback(async () => {
    const revision = ++refreshRevision.current
    try {
      await refreshSocksInfo()
      const [capabilities, status, connectionState] = await Promise.all([
        WtTransport.getCapabilities(),
        WtTransport.getStatus(),
        WtTransport.getConnectionState(),
      ])
      if (revision !== refreshRevision.current) return
      setSnapshot({ capabilities, status, connectionState })
      setRefreshError(undefined)
    } catch (error) {
      if (revision !== refreshRevision.current) return
      if (isBrowserUnsupportedError(error)) return
      setRefreshError(hasWailsHost() || !(error instanceof Error) ? 'Нет связи с локальной службой WhiteTransport.' : error.message)
    }
  }, [refreshSocksInfo])

  useEffect(() => {
    void fetchServers().catch(() => setRefreshError('Не удалось обновить список узлов.'))
    void refreshNative()
  }, [fetchServers, refreshNative])

  useEffect(() => {
    let disposed = false
    const refreshFromHost = () => {
      if (disposed) return
      void fetchServers().catch(() => setRefreshError('Не удалось обновить список узлов.'))
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

  const connectionState = snapshot.connectionState
  const status = snapshot.status
  const isDesktop = snapshot.capabilities?.host === 'wails'
  const socksOnly = isDesktop && snapshot.capabilities?.systemVpn === false
  const socksReady = socksOnly && connectionState?.transportState === 'connected' && Boolean(connectionState.socks) && connectionState.sessionActive !== false
  const lifecycle = socksReady ? 'connected' : resolveLifecycle(connectionState, status, manualError ?? refreshError)
  const systemVpnReady = connectionState?.systemVpnState === 'connected' || status?.systemVpnState === 'connected'
  const sessionActive = Boolean(
    status?.active
    || systemVpnReady
    || connectionState?.sessionActive
    || connectionState?.transportState === 'connected'
    || connectionState?.lifecycle === 'connecting'
    || connectionState?.lifecycle === 'connected'
    || connectionState?.lifecycle === 'degraded'
    || connectionState?.lifecycle === 'disconnecting',
  )
  const authoritativeServerId = connectionState?.selectedNodeId ?? client?.connectedServerId
  const preferredServerId = (endpointId ?? client?.preferredServerId) || undefined
  const selectedServerId = (sessionActive ? authoritativeServerId : undefined) ?? preferredServerId ?? (isDesktop ? undefined : defaultServer?.id)
  const selectedServer = servers.find((server) => server.id === selectedServerId) ?? (isDesktop ? undefined : defaultServer)
  const browserUnsupported = !isHosted() || snapshot.capabilities?.host === 'browser'
  const carrier = carrierName(connectionState?.egressCarrier)
  const recovering = connectionState?.transportState === 'reconnecting'
  const failure = isDesktop ? connectionFailureMessage(connectionState?.errorCode) : undefined
  const sessionId = connectionState?.sessionId
  const bindingLabel = connectionState?.egressBindingLabel
  const currentCheck = sessionActive && trafficCheck?.sessionId === sessionId ? trafficCheck : undefined
  const hasConnectionState = connectionState !== null

  const recordSessionEvent = useCallback((summary: string) => {
    const timestamp = new Date().toISOString()
    setSessionEvents((events) => events.at(-1)?.summary === summary ? events : [...events.slice(-79), { timestamp, summary }])
  }, [])

  useEffect(() => {
    if (!isDesktop || !hasConnectionState) return
    if (lifecycle === 'disconnected') {
      const timestamp = new Date().toISOString()
      setSessionEvents((events) => events.length && events.at(-1)?.summary !== 'Отключено' ? [...events.slice(-79), { timestamp, summary: 'Отключено' }] : events)
      return
    }
    const safeSessionId = /^native-[a-f0-9]{12,64}-\d{10,23}$/.test(sessionId ?? '') ? sessionId : undefined
    recordSessionEvent([
      recovering ? 'Восстановление подключения' : lifecycle === 'connected' && socksOnly ? 'SOCKS подключён' : lifecycleLabels[lifecycle],
      carrier,
      bindingLabel ? `Привязка ${bindingLabel}` : undefined,
      failure,
      safeSessionId,
    ].filter(Boolean).join(' · '))
  }, [bindingLabel, carrier, hasConnectionState, failure, isDesktop, lifecycle, recordSessionEvent, recovering, sessionId, socksOnly])

  const checkTraffic = useCallback(async () => {
    setTrafficCheck({ sessionId, state: 'running' })
    recordSessionEvent('Проверка трафика через SOCKS')
    try {
      const result = await WtTransport.getDesktopStatus()
      if (!result?.externalIp || result.error) {
        setTrafficCheck({ sessionId, state: 'failed' })
        recordSessionEvent('Ответ через SOCKS не получен')
        return
      }
      setTrafficCheck({ sessionId, state: 'passed', ip: result.externalIp })
      recordSessionEvent('Получен ответ через SOCKS')
    } catch {
      setTrafficCheck({ sessionId, state: 'failed' })
      recordSessionEvent('Локальная проверка SOCKS завершилась ошибкой')
    }
  }, [recordSessionEvent, sessionId])

  const selectTransport = async (key: string) => {
    const app = getWailsApp()
    if (!app?.SelectEgressTransport || !sessionId || changingTransport) return
    setChangingTransport(true)
    setManualError(undefined)
    try {
      await app.SelectEgressTransport(sessionId, key)
      setTrafficCheck(undefined)
      await refreshNative()
    } catch {
      setManualError('Не удалось сменить транспорт. Проверьте, что узел подключён.')
    } finally {
      setChangingTransport(false)
    }
  }

  const selectEndpoint = useCallback(async (serverId: string) => {
    if (sessionActive) {
      setManualError('Отключитесь, чтобы выбрать другой узел')
      return
    }
    setEndpointId(serverId)
    if (!clientId) {
      setManualError('Устройство не зарегистрировано')
      return
    }
    await updateClient(clientId, { preferredServerId: serverId || undefined })
  }, [clientId, sessionActive, updateClient])

  const handlePower = useCallback(async () => {
    if (busy) return
    setBusy(sessionActive ? 'disconnect' : 'connect')
    setManualError(undefined)
    try {
      if (!clientId) {
        setManualError('Устройство не зарегистрировано')
        return
      }
      if (isDesktop) {
        if (sessionActive) {
          recordSessionEvent('Отключение по запросу')
          await WtTransport.disconnect()
        } else {
          setSessionEvents([{ timestamp: new Date().toISOString(), summary: 'Подключение по запросу' }])
          setTrafficCheck(undefined)
          await WtTransport.connect({ serverId: preferredServerId, ...(preferredTransport ? { transport: preferredTransport } : {}) })
        }
      } else if (sessionActive) {
        await disconnectClient(clientId)
      } else {
        await connectClient(clientId, selectedServerId)
      }
      await refreshNative()
    } catch (error) {
      const message = sessionActive ? 'Не удалось отключиться' : 'Не удалось подключиться'
      setManualError(!isDesktop && error instanceof Error ? error.message : message)
      if (isDesktop) recordSessionEvent(message)
      await refreshNative()
    } finally {
      setBusy(undefined)
    }
  }, [busy, clientId, connectClient, disconnectClient, isDesktop, preferredServerId, preferredTransport, recordSessionEvent, refreshNative, selectedServerId, sessionActive])

  const handlePermission = useCallback(async () => {
    setBusy('permission')
    setManualError(undefined)
    try {
      await WtTransport.requestVPNPermission()
      await refreshNative()
    } catch (error) {
      setManualError(error instanceof Error ? error.message : 'Разрешение VPN недоступно')
    } finally {
      setBusy(undefined)
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
      setSmokeTestResult(await WtTransport.runSmokeTest({ nodeId: selectedServerId ?? defaultServer?.id }))
    } catch (error) {
      setManualError(error instanceof Error ? error.message : 'Тестовый режим недоступен')
    } finally {
      setSmokeTestBusy(false)
    }
  }, [defaultServer?.id, selectedServerId])

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
            error={failure ?? manualError ?? refreshError}
            browserUnsupported={browserUnsupported}
            busy={busy}
            desktop={isDesktop ? {
              socksOnly,
              socks: connectionState?.socks,
              activeNodeId: connectionState?.selectedNodeId,
              carrier,
              preferredTransport: getWailsApp()?.ConnectWithPolicy ? preferredTransport : undefined,
              onPreferredTransport: setPreferredTransport,
              selection: connectionState?.egressSelection,
              transports: getWailsApp()?.SelectEgressTransport ? connectionState?.egressTransports : undefined,
              pinnedTransportKey: connectionState?.pinnedTransportKey,
              changingTransport,
              onSelectTransport: selectTransport,
              recovering,
              trafficCheck: currentCheck,
              trafficCheckBusy: trafficCheck?.state === 'running',
              onCheckTraffic: checkTraffic,
              events: sessionEvents,
            } : undefined}
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
