import { Check, Globe2, RefreshCw, WifiOff } from 'lucide-react'

import type { RuntimeNode } from '../../native/transport-contract'
import type { Server } from '../../store/client-store'

interface EndpointsScreenProps {
  readonly servers: readonly Server[]
  readonly runtimeNodes: readonly RuntimeNode[]
  readonly selectedServerId?: string
  readonly selectionDisabled: boolean
  readonly refreshing: boolean
  readonly onRefresh: () => void
  readonly onSelect: (serverId: string) => void
}

function availabilityLabel(server: Server): string {
  if (server.status === 'online') return 'Доступен'
  if (server.status === 'degraded') return 'Нестабилен'
  return 'Недоступен'
}

/** Return a grammatically correct Russian node count for the compact list header. */
export function formatNodeCount(count: number): string {
  const absolute = Math.abs(count)
  const lastTwo = absolute % 100
  const last = absolute % 10
  const noun = lastTwo >= 11 && lastTwo <= 14 ? 'узлов' : last === 1 ? 'узел' : last >= 2 && last <= 4 ? 'узла' : 'узлов'
  return `${count} ${noun}`
}

/** Endpoint discovery and manual endpoint editing live here, away from the home action. */
export function EndpointsScreen({ servers, runtimeNodes, selectedServerId, selectionDisabled, refreshing, onRefresh, onSelect }: EndpointsScreenProps) {
  return (
    <section className="shell-screen shell-endpoints" aria-labelledby="endpoints-heading">
      <header className="shell-screen__header">
        <div>
          <p className="shell-eyebrow">Подключение</p>
          <h1 id="endpoints-heading">Endpoints</h1>
        </div>
        <button type="button" className="shell-icon-button" aria-label="Обновить endpoints" onClick={onRefresh} disabled={refreshing}>
          <RefreshCw aria-hidden="true" size={18} className={refreshing ? 'shell-spin' : undefined} />
        </button>
      </header>

      <div className="shell-section-intro">
        <p>Выберите обнаруженный узел. Задержка и возможности обновляются вручную.</p>
        <span className="shell-count">{formatNodeCount(servers.length)}</span>
      </div>

      <div className="shell-endpoint-list" role="list" aria-label="Обнаруженные endpoints">
        {servers.length === 0 ? (
          <div className="shell-empty-state">
            <WifiOff aria-hidden="true" size={24} />
            <strong>Endpoints не обнаружены</strong>
            <p>Обновите список обнаруженных узлов.</p>
          </div>
        ) : servers.map((server) => {
          const selected = server.id === selectedServerId
          const capabilities = runtimeNodes.find((node) => node.node_id === server.id)?.capabilities ?? []
          return (
            <article className={`shell-endpoint-row${selected ? ' shell-endpoint-row--selected' : ''}`} data-testid={`endpoint-${server.id}`} key={server.id} role="listitem">
              <div className="shell-endpoint-row__icon"><Globe2 aria-hidden="true" size={19} /></div>
              <div className="shell-endpoint-row__body">
                <div className="shell-endpoint-row__title">
                  <h2>{server.name}</h2>
                  <span className={`shell-availability shell-availability--${server.status}`}>{availabilityLabel(server)}</span>
                </div>
                <p>{server.country} · {server.city}</p>
                <div className="shell-endpoint-row__meta">
                  <span>{server.ping > 0 ? `${server.ping} ms` : 'Нет ping'}</span>
                  {capabilities.slice(0, 3).map((capability) => <span key={capability}>{capability}</span>)}
                  {capabilities.length === 0 ? <span>auto transport</span> : null}
                </div>
              </div>
              <button
                type="button"
                className="shell-select-button"
                data-testid={`endpoint-select-${server.id}`}
                aria-label={`${selected ? 'Выбран' : 'Выбрать'} ${server.name}`}
                aria-pressed={selected}
                disabled={server.status === 'offline' || selectionDisabled}
                onClick={() => onSelect(server.id)}
              >
                {selected ? <Check aria-hidden="true" size={18} /> : 'Выбрать'}
              </button>
            </article>
          )
        })}
      </div>

    </section>
  )
}
