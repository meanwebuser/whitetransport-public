import { CheckCircle2, ChevronDown, CircleAlert, LoaderCircle, Power, ShieldAlert, WifiOff } from 'lucide-react'

import type { Client, Server } from '../../store/client-store'
import type { ConnectionLifecycle, SystemVPNState } from '../../native/transport-contract'

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
  readonly busy: boolean
  readonly onPower: () => void
  readonly onPermission: () => void
  readonly onEndpointChange: (serverId: string) => void
  readonly onOpenEndpoints: () => void
}

const lifecycleLabels: Record<ConnectionLifecycle, string> = {
  disconnected: 'Отключено',
  permission_required: 'Требуется разрешение',
  connecting: 'Подключение…',
  connected: 'Подключено',
  degraded: 'Нестабильно',
  disconnecting: 'Отключение…',
  unsupported: 'Не поддерживается',
  error: 'Ошибка подключения',
}

function statusIcon(lifecycle: ConnectionLifecycle) {
  if (lifecycle === 'connected') return <CheckCircle2 aria-hidden="true" size={17} />
  if (lifecycle === 'connecting' || lifecycle === 'disconnecting') return <LoaderCircle aria-hidden="true" className="shell-spin" size={17} />
  if (lifecycle === 'degraded' || lifecycle === 'error' || lifecycle === 'permission_required') return <CircleAlert aria-hidden="true" size={17} />
  return <WifiOff aria-hidden="true" size={17} />
}

function powerLabel(lifecycle: ConnectionLifecycle, busy: boolean, sessionActive: boolean): string {
  if (lifecycle === 'disconnecting' || (busy && sessionActive)) return 'Отключение…'
  if (busy || lifecycle === 'connecting') return 'Подключение…'
  if (sessionActive) return 'Отключить'
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
  onPower,
  onPermission,
  onEndpointChange,
  onOpenEndpoints,
}: HomeScreenProps) {
  const connected = lifecycle === 'connected'
  const permissionRequired = lifecycle === 'permission_required' || systemVpnState === 'permission_required'
  const buttonAction = permissionRequired ? onPermission : onPower

  return (
    <section className="shell-screen shell-home" aria-labelledby="home-heading">
      <header className="shell-screen__header">
        <div>
          <p className="shell-eyebrow">WhiteTransport</p>
          <h1 id="home-heading">Главная</h1>
        </div>
        <span className={`shell-status-chip shell-status-chip--${lifecycle}`} data-testid="connection-status" role="status" aria-live="polite">
          {statusIcon(lifecycle)}
          <span>{lifecycleLabels[lifecycle]}</span>
        </span>
      </header>

      {browserUnsupported ? (
        <div className="shell-setup-alert" data-testid="browser-unsupported" role="alert">
          <ShieldAlert aria-hidden="true" size={20} />
          <div>
            <strong>Браузер не поддерживается</strong>
            <p>Откройте приложение в Wails или Capacitor, чтобы управлять системным VPN.</p>
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
          aria-label={powerLabel(lifecycle, busy, sessionActive)}
          aria-pressed={connected}
          disabled={browserUnsupported || !clientAvailable || busy || lifecycle === 'connecting' || lifecycle === 'disconnecting' || lifecycle === 'unsupported'}
          onClick={buttonAction}
        >
          <span className="shell-power-button__halo" aria-hidden="true" />
          <span className="shell-power-button__icon"><Power size={40} strokeWidth={2.1} /></span>
          <span className="shell-power-button__label">{powerLabel(lifecycle, busy, sessionActive)}</span>
        </button>
        <p className="shell-power-hint">
          {connected ? 'Системный VPN активен' : 'Подключение защищает трафик устройства'}
        </p>
      </div>

      <article className="shell-endpoint-card" data-testid="selected-endpoint">
        <div className="shell-card-heading">
          <div>
            <p className="shell-card-kicker">Текущий endpoint</p>
            <h2>{selectedServer?.name ?? 'Endpoint не выбран'}</h2>
          </div>
          {selectedServer ? <span className={`shell-health-dot shell-health-dot--${selectedServer.healthStatus}`} aria-label={selectedServer.healthStatus} /> : null}
        </div>
        {servers.length > 0 ? (
          <label className="shell-select-wrap">
            <span className="sr-only">Выбранный endpoint</span>
            <select
              aria-label="Выбранный endpoint"
              data-testid="selected-endpoint-select"
              value={selectedServerId ?? ''}
              disabled={sessionActive}
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
          <span>{selectedServer?.country ?? 'Автоматически'}</span>
          <button type="button" className="shell-text-button" onClick={onOpenEndpoints}>Все endpoints</button>
        </div>
      </article>

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
        {client?.socksPort ? (
          <div>
            <dt>Локальный порт</dt>
            <dd>{client.socksPort}</dd>
          </div>
        ) : null}
      </dl>
    </section>
  )
}
