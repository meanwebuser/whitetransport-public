import { CheckCircle2, ChevronDown, CircleAlert, LoaderCircle, Power, ShieldAlert, WifiOff } from 'lucide-react'

import type { Client, Server } from '../../store/client-store'
import type { ConnectionLifecycle, SystemVPNState, WtSocksInfo } from '../../native/transport-contract'
import { lifecycleLabels, SessionDiagnostics, type SessionEvent } from './session-diagnostics'

interface DesktopHomeState {
  readonly socksOnly: boolean
  readonly socks?: WtSocksInfo
  readonly activeNodeId?: string
  readonly carrier?: string
  readonly selection?: 'automatic' | 'manual'
  readonly recovering: boolean
  readonly trafficCheck?: { readonly state: 'running' | 'passed' | 'failed'; readonly ip?: string }
  readonly trafficCheckBusy: boolean
  readonly onCheckTraffic: () => void
  readonly events: readonly SessionEvent[]
}

interface HomeScreenProps {
  readonly client?: Client
  readonly servers: readonly Server[]
  readonly selectedServer?: Server
  readonly selectedServerId?: string
  readonly clientAvailable: boolean
  readonly lifecycle: ConnectionLifecycle
  readonly sessionActive: boolean
  readonly systemVpnState: SystemVPNState
  readonly transportState: string
  readonly latency?: number
  readonly error?: string
  readonly browserUnsupported: boolean
  readonly busy?: 'connect' | 'disconnect' | 'permission'
  readonly desktop?: DesktopHomeState
  readonly onPower: () => void
  readonly onPermission: () => void
  readonly onEndpointChange: (serverId: string) => void
  readonly onOpenEndpoints: () => void
}

function statusIcon(lifecycle: ConnectionLifecycle) {
  if (lifecycle === 'connected') return <CheckCircle2 aria-hidden="true" size={17} />
  if (lifecycle === 'connecting' || lifecycle === 'disconnecting') return <LoaderCircle aria-hidden="true" className="shell-spin" size={17} />
  if (lifecycle === 'degraded' || lifecycle === 'error' || lifecycle === 'permission_required') return <CircleAlert aria-hidden="true" size={17} />
  return <WifiOff aria-hidden="true" size={17} />
}

function powerLabel(lifecycle: ConnectionLifecycle, busy: HomeScreenProps['busy'], sessionActive: boolean): string {
  if (lifecycle === 'disconnecting' || busy === 'disconnect') return 'Отключение…'
  if (busy === 'connect') return 'Подключение…'
  if (busy === 'permission') return 'Запрашиваем разрешение…'
  if (sessionActive) return 'Отключить'
  if (busy || lifecycle === 'connecting') return 'Подключение…'
  if (lifecycle === 'permission_required') return 'Разрешить VPN'
  if (lifecycle === 'unsupported') return 'VPN недоступен'
  if (lifecycle === 'degraded' || lifecycle === 'error') return 'Повторить подключение'
  return 'Подключиться'
}

/** Calm home screen: one lifecycle action, selected endpoint, and a small diagnostic readout. */
export function HomeScreen({
  client,
  servers,
  selectedServer,
  selectedServerId,
  clientAvailable,
  lifecycle,
  sessionActive,
  systemVpnState,
  transportState,
  latency,
  error,
  browserUnsupported,
  busy,
  desktop,
  onPower,
  onPermission,
  onEndpointChange,
  onOpenEndpoints,
}: HomeScreenProps) {
  const connected = lifecycle === 'connected'
  const permissionRequired = lifecycle === 'permission_required' || systemVpnState === 'permission_required'
  const buttonAction = permissionRequired ? onPermission : onPower
  const label = powerLabel(lifecycle, busy, sessionActive)
  const status = desktop?.recovering ? 'Восстанавливаем связь…' : connected && desktop?.socksOnly ? 'SOCKS подключён' : lifecycleLabels[lifecycle]
  const nodeLabel = selectedServer?.name || (desktop ? desktop.activeNodeId || (sessionActive ? 'Ожидаем узел' : 'Узел') : 'Endpoint не выбран')

  return (
    <section className={`shell-screen shell-home${desktop ? ' shell-home--desktop' : ''}`} aria-labelledby="home-heading">
      <header className="shell-screen__header">
        <div>
          <h1 id="home-heading">{desktop ? 'Подключение' : 'Главная'}</h1>
        </div>
        <span className={`shell-status-chip shell-status-chip--${lifecycle}`} data-testid="connection-status" role="status" aria-live="polite">
          {statusIcon(lifecycle)}
          <span>{status}</span>
        </span>
      </header>

      {browserUnsupported ? (
        <div className="shell-setup-alert" data-testid="browser-unsupported" role="alert">
          <ShieldAlert aria-hidden="true" size={20} />
          <div>
            <strong>Браузер не поддерживается</strong>
            <p>Управление подключением доступно в установленном WhiteTransport.</p>
          </div>
        </div>
      ) : null}

      {error ? (
        <div className="shell-inline-alert" role="alert">
          <CircleAlert aria-hidden="true" size={17} />
          <span>{error}</span>
        </div>
      ) : null}

      {!clientAvailable ? (
        <div className="shell-setup-alert" data-testid="client-unavailable" role="alert">
          <WifiOff aria-hidden="true" size={20} />
          <div>
            <strong>Устройство не зарегистрировано</strong>
            <p>Нативный runtime ещё не предоставил профиль клиента. Подключение недоступно.</p>
          </div>
        </div>
      ) : null}

      <div className="shell-power-wrap">
        <button
          type="button"
          className={`shell-power-button${connected ? ' shell-power-button--connected' : ''}`}
          data-testid="power-control"
          aria-label={label}
          aria-pressed={connected}
          disabled={browserUnsupported || !clientAvailable || Boolean(busy) || (lifecycle === 'connecting' && !sessionActive) || lifecycle === 'disconnecting' || lifecycle === 'unsupported'}
          onClick={buttonAction}
        >
          <span className="shell-power-button__halo" aria-hidden="true" />
          <span className="shell-power-button__icon"><Power size={desktop ? 19 : 40} strokeWidth={2.1} /></span>
          <span className="shell-power-button__label">{label}</span>
        </button>
        <p className="shell-power-hint">
          {desktop?.socksOnly ? 'В режиме SOCKS трафик идёт через приложения, в которых задан прокси.' : connected ? 'Системный VPN активен' : 'Подключение защищает трафик устройства'}
        </p>
      </div>

      <article className="shell-endpoint-card" data-testid="selected-endpoint">
        <div className="shell-card-heading">
          <div>
            {desktop ? (sessionActive ? <p className="shell-card-kicker">Активный узел</p> : null) : <p className="shell-card-kicker">Текущий endpoint</p>}
            <h2>{nodeLabel}</h2>
          </div>
          {selectedServer ? <span className={`shell-health-dot shell-health-dot--${selectedServer.healthStatus}`} aria-label={selectedServer.healthStatus} /> : null}
        </div>
        {servers.length > 0 ? (
          <label className="shell-select-wrap">
            <span className="sr-only">{desktop ? 'Выбрать узел' : 'Выбранный endpoint'}</span>
            <select
              aria-label={desktop ? 'Выбрать узел' : 'Выбранный endpoint'}
              data-testid="selected-endpoint-select"
              value={selectedServerId ?? ''}
              disabled={sessionActive || Boolean(busy)}
              onChange={(event) => onEndpointChange(event.target.value)}
            >
              <option value="">Автоматический выбор</option>
              {servers.map((server) => (
                <option key={server.id} value={server.id} disabled={server.status === 'offline'}>
                  {server.name} · {server.ping > 0 ? `${server.ping} ms` : 'нет ping'}
                </option>
              ))}
            </select>
            <ChevronDown aria-hidden="true" size={17} />
          </label>
        ) : (
          <p className="shell-muted">Узлы ещё не обнаружены.</p>
        )}
        <div className="shell-endpoint-meta">
          <span>{selectedServer?.country ?? (desktop ? undefined : 'Автоматически')}</span>
          <button type="button" className="shell-text-button" onClick={onOpenEndpoints}>{desktop ? 'Все узлы' : 'Все endpoints'}</button>
        </div>
      </article>

      {desktop ? (
        <>
          <dl className="shell-diagnostics" data-testid="diagnostic-summary">
            <div><dt>Режим</dt><dd>{desktop.socksOnly ? 'SOCKS5' : 'Системный VPN'}{systemVpnState === 'connected' ? <small>Активен</small> : null}</dd></div>
            <div><dt>Транспорт</dt><dd>{desktop.carrier ?? '—'}{desktop.selection === 'manual' ? <small>Выбран вручную</small> : null}</dd></div>
            <div><dt>Адрес SOCKS5</dt><dd>{desktop.socks ? <code>{desktop.socks.host}:{desktop.socks.port}</code> : 'Недоступен'}</dd></div>
            <div>
              <dt>Последняя проверка SOCKS</dt>
              <dd>{desktop.trafficCheck?.ip ?? (desktop.trafficCheck?.state === 'failed' ? 'Нет ответа' : desktop.trafficCheck?.state === 'running' ? 'Проверяем…' : 'Не проверен')}</dd>
            </div>
          </dl>
          {connected && desktop.socks ? (
            <div className="shell-traffic-check">
              <button type="button" className="shell-secondary-button" disabled={desktop.trafficCheckBusy} onClick={desktop.onCheckTraffic}>Проверить трафик</button>
              {desktop.trafficCheck?.state === 'failed' ? <span role="status">Запрос через SOCKS не получил ответ. Повторите проверку после восстановления связи.</span> : null}
            </div>
          ) : null}
          <SessionDiagnostics events={desktop.events} />
        </>
      ) : (
      <dl className="shell-diagnostics" data-testid="diagnostic-summary">
        <div>
          <dt>Задержка</dt>
          <dd>{latency && latency > 0 ? `${latency} ms` : '—'}</dd>
        </div>
        <div>
          <dt>Транспорт</dt>
          <dd>{transportState || '—'}</dd>
        </div>
        <div>
          <dt>VPN</dt>
          <dd>{systemVpnState === 'connected' ? 'Активен' : systemVpnState === 'unsupported' ? 'Не поддерживается' : systemVpnState}</dd>
        </div>
        <div>
          <dt>Локальный порт</dt>
          <dd>{client?.socksPort || 'Недоступен'}</dd>
        </div>
      </dl>
      )}
    </section>
  )
}
