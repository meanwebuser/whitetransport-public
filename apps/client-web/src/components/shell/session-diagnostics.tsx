import { Clipboard } from 'lucide-react'
import { useState } from 'react'

import type { ConnectionLifecycle } from '../../native/transport-contract'

export interface SessionEvent {
  readonly timestamp: string
  readonly summary: string
}

export const lifecycleLabels: Record<ConnectionLifecycle, string> = {
  disconnected: 'Отключено',
  permission_required: 'Требуется разрешение',
  connecting: 'Подключение…',
  connected: 'Подключено',
  degraded: 'Нестабильно',
  disconnecting: 'Отключение…',
  unsupported: 'Подключение недоступно',
  error: 'Ошибка подключения',
}

export function carrierName(carrier?: string): string | undefined {
  switch (carrier) {
    case 'vk_messages': return 'VK Messages'
    case 'vk_documents': return 'VK Documents'
    case 'telemost': return 'Телемост'
    case 'dion': return 'DION'
    case 'vless': return 'VLESS'
    case 'hysteria2': return 'Hysteria2'
    case 'other': return 'Другой транспорт'
    default: return undefined
  }
}

export function connectionFailureMessage(code?: string): string | undefined {
  switch (code) {
    case 'credential_missing': return 'Для маршрута нужна настройка аккаунта.'
    case 'authentication_failed': return 'Сервис отклонил вход. Проверьте аккаунт.'
    case 'rate_limited': return 'Сервис ограничил запросы. Подождите и проверьте маршрут снова.'
    case 'timeout': return 'Узел или сервис не ответил вовремя.'
    case 'no_available_nodes':
    case 'node_unavailable': return 'Узел недоступен. Проверьте список узлов.'
    case 'no_active_session': return 'Сессия прервалась. Подождите восстановления связи.'
    case 'no_compatible_egress': return 'У этого узла нет доступного транспорта для трафика.'
    case 'requested_transport_unavailable': return 'Выбранный транспорт недоступен на этом узле.'
    case 'carrier_routes_exhausted': return 'Транспорты узла временно недоступны. Приложение ищет рабочий маршрут.'
    case 'egress_return_failed': return 'Узел не смог передать ответ через выбранный транспорт.'
    case 'tunnel_unhealthy': return 'Туннель перестал отвечать.'
    case 'runtime_error':
    case 'connection_failed': return 'Не удалось открыть маршрут.'
    default: return undefined
  }
}

export function SessionDiagnostics({ events }: { readonly events: readonly SessionEvent[] }) {
  const [copyStatus, setCopyStatus] = useState('')

  const copy = async () => {
    const text = events.map(({ timestamp, summary }) => `${timestamp}  ${summary}`).join('\n')
    try {
      const runtime = window.runtime
      if (runtime?.ClipboardSetText) {
        if (!await runtime.ClipboardSetText(text)) throw new Error('Clipboard unavailable')
      } else {
        await navigator.clipboard.writeText(text)
      }
      setCopyStatus('Скопировано')
    } catch {
      setCopyStatus('Не удалось скопировать. Выделите строки журнала вручную.')
    }
  }

  return (
    <details className="shell-diagnostics-details shell-session-diagnostics">
      <summary>Журнал подключения</summary>
      <div className="shell-details-content">
        {events.length ? (
          <>
            <ol className="shell-session-events">
              {events.map(({ timestamp, summary }, index) => (
                <li key={`${timestamp}-${index}`}><time dateTime={timestamp}>{new Date(timestamp).toLocaleTimeString('ru-RU')}</time><span>{summary}</span></li>
              ))}
            </ol>
            <button type="button" className="shell-secondary-button" onClick={() => { void copy() }}>
              <Clipboard size={15} aria-hidden="true" /> Скопировать журнал
            </button>
            {copyStatus ? <p className="shell-muted-text" role="status">{copyStatus}</p> : null}
          </>
        ) : <p>Здесь появятся события после первой попытки подключения.</p>}
      </div>
    </details>
  )
}
