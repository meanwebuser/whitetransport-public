import { Check, Clipboard, FileText, FlaskConical, KeyRound, ShieldCheck, Terminal, TriangleAlert } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import type { SmokeTestResult, WtCapabilities, WtLogInfo, WtRoutingSettings } from '../../native/transport-contract'

type UiCapabilities = WtCapabilities & {
  readonly dns?: boolean
  readonly killSwitch?: boolean
}

interface SettingsScreenProps {
  readonly capabilities: UiCapabilities | null
  readonly splitRouting: WtRoutingSettings | null
  readonly logs: WtLogInfo | null
  readonly logError?: string
  readonly actionError?: string
  readonly loadingSplitRouting: boolean
  readonly onLoadSplitRouting: () => void
  readonly onSetSplitRouting: (settings: WtRoutingSettings) => void
  readonly onLoadLogs: () => void
  readonly smokeTestResult: SmokeTestResult | null
  readonly smokeTestBusy: boolean
  readonly onRunSmokeTest: () => Promise<void>
}

/** Normalize package identifiers before sending them to a native coordinator. */
export function normalizePackageIds(value: string | readonly string[]): string[] {
  const values: readonly string[] = typeof value === 'string' ? value.split(/[\n,]/) : value
  return [...new Set(values.map((item: string) => item.trim()).filter(Boolean))]
}

/** Normalize CIDR destinations before persisting a desktop split policy. */
export function normalizeDestinationIds(value: string | readonly string[]): string[] {
  const values: readonly string[] = typeof value === 'string' ? value.split(/[\n,]/) : value
  return [...new Set(values.map((item: string) => item.trim()).filter(Boolean))]
}

const modeLabels: Record<string, string> = {
  none: 'Весь трафик',
  bypass: 'Исключения (bypass)',
}

function splitModeLabel(mode: string, host?: string): string {
  if (mode === 'only') {
    return host === 'wails' ? 'Только выбранные сети / CIDR' : 'Только выбранные приложения'
  }

  return modeLabels[mode] ?? mode
}

function sanitizeSmokeTestResult(result: SmokeTestResult) {
  return {
    passed: result.passed,
    totalDurationMs: result.totalDurationMs,
    summary: result.summary,
    selectedNodeId: result.selectedNodeId,
    directIp: result.directIp,
    externalIp: result.externalIp,
    socksIp: result.socksIp,
    tunnelIp: result.tunnelIp,
    latencyMs: result.latencyMs,
    logPath: result.logPath,
    resultPath: result.resultPath,
    steps: result.steps.map((step) => ({
      name: step.name,
      status: step.status,
      durationMs: step.durationMs,
    })),
  }
}

/** Settings hold optional routing, diagnostics, and test-mode controls. */
export function SettingsScreen({
  capabilities,
  splitRouting,
  logs,
  logError,
  actionError,
  loadingSplitRouting,
  onLoadSplitRouting,
  onSetSplitRouting,
  onLoadLogs,
  smokeTestResult,
  smokeTestBusy,
  onRunSmokeTest,
}: SettingsScreenProps) {
  const [logsVisible, setLogsVisible] = useState(false)
  const [packages, setPackages] = useState('')
  const [destinations, setDestinations] = useState('')
  const [draftMode, setDraftMode] = useState('none')
  const [draftLanAccess, setDraftLanAccess] = useState(false)
  const supportsSplitRouting = capabilities?.splitRouting === true
  const usesDesktopDestinations = supportsSplitRouting && capabilities?.host === 'wails'
  const usesAndroidPackages = supportsSplitRouting && capabilities?.host === 'capacitor'
  const supportsLanAccess = supportsSplitRouting && capabilities?.host === 'wails'
  const logPath = logs?.path ?? 'Путь появится после запуска runtime'
  const logText = useMemo(() => logs?.lines.join('\n') ?? '', [logs?.lines])

  useEffect(() => {
    if (supportsSplitRouting && !splitRouting) onLoadSplitRouting()
  }, [onLoadSplitRouting, splitRouting, supportsSplitRouting])

  useEffect(() => {
    if (!splitRouting) return
    setDraftMode(splitRouting.mode)
    setDraftLanAccess(splitRouting.lan_access)
    setPackages(splitRouting.packages?.join('\n') ?? '')
    setDestinations(splitRouting.destinations?.join('\n') ?? '')
  }, [splitRouting])

  const draftSettings = (mode: string, lanAccess: boolean): WtRoutingSettings => ({
    mode,
    lan_access: supportsLanAccess ? lanAccess : false,
    ...(usesDesktopDestinations ? { destinations: normalizeDestinationIds(destinations) } : {}),
    ...(usesAndroidPackages ? { packages: normalizePackageIds(packages) } : {}),
  })

  const applySplitRouting = () => {
    onSetSplitRouting(draftSettings(draftMode, draftLanAccess))
  }

  return (
    <section className="shell-screen shell-settings" aria-labelledby="settings-heading">
      <header className="shell-screen__header">
        <div>
          <p className="shell-eyebrow">Приложение</p>
          <h1 id="settings-heading">Настройки</h1>
        </div>
        <KeyRound aria-hidden="true" className="shell-header-icon" size={22} />
      </header>

      <section className="shell-settings-card" aria-labelledby="capabilities-heading">
        <div className="shell-card-heading">
          <div>
            <p className="shell-card-kicker">Возможности хоста</p>
            <h2 id="capabilities-heading">Системные функции</h2>
          </div>
          <ShieldCheck aria-hidden="true" size={22} />
        </div>
        <div className="shell-capability-grid">
          <CapabilityIndicator label="System VPN" value={capabilities?.systemVpn === true} />
          <CapabilityIndicator label="DNS" value={capabilities?.dns === true} />
          <CapabilityIndicator label="Kill switch" value={capabilities?.killSwitch === true} />
          <CapabilityIndicator label="Logs" value={capabilities?.logs === true} />
        </div>
      </section>

      {supportsSplitRouting ? (
        <section className="shell-settings-card" data-testid="split-routing-section" aria-labelledby="split-routing-heading">
          <div className="shell-card-heading">
            <div>
              <p className="shell-card-kicker">Маршрутизация</p>
              <h2 id="split-routing-heading">Раздельный трафик</h2>
            </div>
            <span className="shell-supported-badge">Доступно</span>
          </div>
          <label className="shell-field">
            <span>Режим</span>
            <select
              data-testid="split-routing-mode"
              value={draftMode}
              disabled={loadingSplitRouting}
              onChange={(event) => {
                const mode = event.target.value
                setDraftMode(mode)
                onSetSplitRouting(draftSettings(mode, draftLanAccess))
              }}
            >
              {Object.entries(modeLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}
              <option value="only">{splitModeLabel('only', capabilities?.host)}</option>
            </select>
          </label>
          {supportsLanAccess ? (
            <label className="shell-checkbox-row">
              <input
                type="checkbox"
                checked={draftLanAccess}
                disabled={loadingSplitRouting}
                onChange={(event) => {
                  const lanAccess = event.target.checked
                  setDraftLanAccess(lanAccess)
                  onSetSplitRouting(draftSettings(draftMode, lanAccess))
                }}
              />
              <span>Разрешить доступ к локальной сети</span>
            </label>
          ) : null}
          {usesDesktopDestinations ? (
            <label className="shell-field" data-testid="split-destination-settings">
              <span>Целевые сети (CIDR)</span>
              <textarea
                value={destinations}
                onChange={(event) => setDestinations(event.target.value)}
                placeholder="198.51.100.0/24 или 2001:db8::/32"
                rows={3}
              />
            </label>
          ) : null}
          {usesAndroidPackages ? (
            <label className="shell-field" data-testid="split-package-settings">
              <span>Пакеты приложений</span>
              <textarea
                value={packages}
                onChange={(event) => setPackages(event.target.value)}
                placeholder="По одному имени пакета на строку"
                rows={3}
              />
            </label>
          ) : null}
          <button type="button" className="shell-primary-button shell-split-apply" data-testid="split-routing-apply" onClick={applySplitRouting} disabled={loadingSplitRouting}>
            Применить routing
          </button>
          {actionError ? <p className="shell-inline-alert" role="alert"><TriangleAlert aria-hidden="true" size={16} /> {actionError}</p> : null}
        </section>
      ) : null}

      <section className="shell-settings-card shell-logs-card" aria-labelledby="logs-heading">
        <div className="shell-card-heading">
          <div>
            <p className="shell-card-kicker">Диагностика</p>
            <h2 id="logs-heading">Логи runtime</h2>
          </div>
          <FileText aria-hidden="true" size={21} />
        </div>
        <p className="shell-log-path"><span>Путь</span><code>{logPath}</code></p>
        <div className="shell-inline-actions">
          <button type="button" className="shell-secondary-button" onClick={() => { setLogsVisible((visible) => !visible); if (!logs) onLoadLogs() }}>
            <Terminal aria-hidden="true" size={16} /> {logsVisible ? 'Скрыть логи' : 'Показать логи'}
          </button>
          {logsVisible && logs ? (
            <>
              <button type="button" className="shell-secondary-button" onClick={() => void navigator.clipboard?.writeText(logText)}>
                <Clipboard aria-hidden="true" size={16} /> Копировать
              </button>
              <a className="shell-secondary-button" href={`data:text/plain;charset=utf-8,${encodeURIComponent(logText)}`} download="whitetransport-log.txt">
                Экспорт
              </a>
            </>
          ) : null}
        </div>
        {logError ? <p className="shell-inline-alert"><TriangleAlert aria-hidden="true" size={16} /> {logError}</p> : null}
        {logsVisible && logs ? <pre className="shell-log-lines" data-testid="log-lines">{logText || 'Лог пока пуст.'}</pre> : null}
      </section>

      {capabilities?.smokeTest === true ? (
        <section className="shell-settings-card" data-testid="test-mode-section" aria-labelledby="test-mode-heading">
          <div className="shell-card-heading">
            <div>
              <p className="shell-card-kicker">Проверка подключения</p>
              <h2 id="test-mode-heading">Тестовый режим</h2>
            </div>
            <FlaskConical aria-hidden="true" size={21} />
          </div>
          <p>Запускает connect → telemetry → disconnect через тот же runtime API и сохраняет санитизированный JSON-результат без секретов.</p>
          <button type="button" className="shell-primary-button" data-testid="test-mode-run" onClick={() => void onRunSmokeTest()} disabled={smokeTestBusy}>
            {smokeTestBusy ? 'Проверка…' : 'Запустить проверку'}
          </button>
          {smokeTestResult ? (
            <div className="shell-details-content" data-testid="test-mode-result">
              <strong>{smokeTestResult.passed ? 'Проверка пройдена' : 'Проверка завершилась ошибкой'}</strong>
              <p>{smokeTestResult.summary}</p>
              <dl className="shell-details-list">
                <div><dt>Узел</dt><dd>{smokeTestResult.selectedNodeId ? <code>{smokeTestResult.selectedNodeId}</code> : '—'}</dd></div>
                <div><dt>Direct IP</dt><dd>{smokeTestResult.directIp ? <code>{smokeTestResult.directIp}</code> : smokeTestResult.externalIp ? <code>{smokeTestResult.externalIp}</code> : '—'}</dd></div>
                <div><dt>External IP</dt><dd>{smokeTestResult.externalIp ? <code>{smokeTestResult.externalIp}</code> : '—'}</dd></div>
                <div><dt>SOCKS IP</dt><dd>{smokeTestResult.socksIp ? <code>{smokeTestResult.socksIp}</code> : smokeTestResult.tunnelIp ? <code>{smokeTestResult.tunnelIp}</code> : '—'}</dd></div>
                <div><dt>Tunnel IP</dt><dd>{smokeTestResult.tunnelIp ? <code>{smokeTestResult.tunnelIp}</code> : '—'}</dd></div>
                <div><dt>Latency</dt><dd>{typeof smokeTestResult.latencyMs === 'number' ? `${Math.round(smokeTestResult.latencyMs)}ms` : '—'}</dd></div>
                <div><dt>Лог</dt><dd>{smokeTestResult.logPath ? <code>{smokeTestResult.logPath}</code> : '—'}</dd></div>
                <div><dt>Результат</dt><dd>{smokeTestResult.resultPath ? <code>{smokeTestResult.resultPath}</code> : '—'}</dd></div>
              </dl>
              {smokeTestResult.steps.length ? (
                <div className="shell-details-list" data-testid="test-mode-steps">
                  {smokeTestResult.steps.map((step) => (
                    <div key={step.name}>
                      <strong>{step.name}</strong>
                      <span> {step.status}</span>
                      <span> · {step.durationMs}ms</span>
                    </div>
                  ))}
                </div>
              ) : null}
              <details data-testid="test-mode-result-json">
                <summary>Санитизированный JSON</summary>
                <pre>{JSON.stringify(sanitizeSmokeTestResult(smokeTestResult), null, 2)}</pre>
              </details>
            </div>
          ) : null}
        </section>
      ) : null}

      <details className="shell-settings-card shell-diagnostics-details" data-testid="diagnostics-details">
        <summary data-testid="diagnostics-summary"><FlaskConical aria-hidden="true" size={17} /> Тестовый режим и расширенная диагностика</summary>
        <div className="shell-details-content">
          <p>Для хостов без интерактивной кнопки используйте debug launch contract; Android оставляет smoke test недоступным до установленного TUN/payload proof.</p>
          <dl className="shell-details-list">
            <div><dt>Test mode</dt><dd>{capabilities?.smokeTest === true ? 'Доступен' : 'Не заявлен хостом'}</dd></div>
            <div><dt>Секреты</dt><dd><Check aria-hidden="true" size={15} /> Не отображаются</dd></div>
          </dl>
        </div>
      </details>
    </section>
  )
}

function CapabilityIndicator({ label, value }: { readonly label: string; readonly value: boolean }) {
  return (
    <div className="shell-capability-indicator">
      <span className={`shell-capability-dot${value ? ' shell-capability-dot--on' : ''}`} aria-hidden="true" />
      <span>{label}</span>
      <strong>{value ? 'Да' : 'Нет'}</strong>
    </div>
  )
}
