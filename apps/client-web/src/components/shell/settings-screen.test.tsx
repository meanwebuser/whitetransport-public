// @vitest-environment jsdom

import '@testing-library/jest-dom/vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { SmokeTestResult, WtCapabilities, WtLogInfo, WtRoutingSettings } from '../../native/transport-contract'

import { SettingsScreen } from './settings-screen'

const baseCapabilities: WtCapabilities & { readonly dns?: boolean; readonly killSwitch?: boolean } = {
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
  dns: true,
  killSwitch: true,
}

const splitRouting: WtRoutingSettings = {
  mode: 'only',
  lan_access: false,
  destinations: ['198.51.100.0/24'],
}

const logs: WtLogInfo = {
  path: '/var/log/whitetransport.jsonl',
  lines: ['ready'],
  persistent: true,
}

const smokeTestResult: SmokeTestResult = {
  passed: false,
  totalDurationMs: 1284,
  summary: 'Smoke test failed on node selection',
  steps: [
    { name: 'discover node', status: 'pass', durationMs: 91 },
    { name: 'open tunnel', status: 'fail', durationMs: 233, detail: 'node-berlin', error: 'timeout' },
    { name: 'verify egress', status: 'skip', durationMs: 0 },
  ],
  directIp: '198.51.100.10',
  externalIp: '198.51.100.11',
  socksIp: '203.0.113.10',
  tunnelIp: '203.0.113.11',
  latencyMs: 42,
  selectedNodeId: 'node-berlin',
  logPath: '/tmp/WhiteTransport.log',
  resultPath: '/tmp/whitetransport-test-result.json',
}

function renderSettingsScreen(overrides: Partial<React.ComponentProps<typeof SettingsScreen>> = {}) {
  const props: React.ComponentProps<typeof SettingsScreen> = {
    capabilities: baseCapabilities,
    splitRouting,
    logs,
    loadingSplitRouting: false,
    onLoadSplitRouting: vi.fn(),
    onSetSplitRouting: vi.fn(),
    onLoadLogs: vi.fn(),
    smokeTestResult: null,
    smokeTestBusy: false,
    onRunSmokeTest: vi.fn(),
    ...overrides,
  }

  return render(<SettingsScreen {...props} />)
}

describe('SettingsScreen', () => {
  afterEach(() => {
    cleanup()
    delete window.go
  })

  it('shows a desktop split label that refers to networks and CIDR destinations', () => {
    renderSettingsScreen()

    const modeSelect = screen.getByTestId('split-routing-mode')
    expect(within(modeSelect).getByRole('option', { name: /сети.*CIDR/i })).toBeInTheDocument()
  })

  it('keeps the Android split label on applications wording', () => {
    renderSettingsScreen({
      capabilities: { ...baseCapabilities, host: 'capacitor' },
    })

    const modeSelect = screen.getByTestId('split-routing-mode')
    expect(within(modeSelect).getByRole('option', { name: /приложен/i })).toBeInTheDocument()
  })

  it('shows desktop room-first readiness without exposing credential values', () => {
    renderSettingsScreen()

    const readiness = screen.getByTestId('room-first-readiness')
    expect(readiness).toHaveTextContent(/room-first/i)
    expect(readiness).toHaveTextContent(/встроенный логин/i)
    expect(screen.getByTestId('desktop-room-auth-button')).toHaveTextContent(/войти в WB Stream/i)
    expect(readiness).not.toHaveTextContent(/cookie|token|парол/i)
  })

  it('offers the Android built-in WBStream login without credential fields', () => {
    renderSettingsScreen({
      capabilities: { ...baseCapabilities, host: 'capacitor' },
    })

    const roomAuth = screen.getByTestId('room-auth-readiness')
    expect(roomAuth).toHaveTextContent(/room-first/i)
    expect(screen.getByTestId('room-auth-button')).toHaveTextContent(/войти в WB Stream/i)
    expect(roomAuth).not.toHaveTextContent(/cookie|token|парол/i)
    expect(screen.queryByRole('link', { name: 'Скачать расширение' })).not.toBeInTheDocument()
  })

  it('opens the public extension release in the system browser', async () => {
    const openURL = vi.fn()
    Object.defineProperty(window, 'runtime', { value: { BrowserOpenURL: openURL }, configurable: true })
    try {
      renderSettingsScreen()
      const link = screen.getByRole('link', { name: 'Скачать расширение' })
      expect(link).toHaveAttribute('href', 'https://github.com/meanwebuser/whitetransport-public/releases/tag/v0.1.8')
      await userEvent.click(link)
      expect(openURL).toHaveBeenCalledWith(link.getAttribute('href'))
    } finally {
      Reflect.deleteProperty(window, 'runtime')
    }
  })

  it('reads room-first readiness from the local Wails bridge', async () => {
    window.go = {
      main: {
        App: {
          HasClientRoomCredentials: vi.fn().mockResolvedValue(true),
          GetClientCredentialRefreshMessage: vi.fn().mockResolvedValue('Сессия обновлена локально.'),
        } as never,
      },
    }

    renderSettingsScreen()

    expect(await screen.findByText(/Готово: после подключения клиент создаст комнату/i)).toBeInTheDocument()
    expect(screen.getByText('Сессия обновлена локально.')).toBeInTheDocument()
  })

  it('imports the chosen browser export through Wails and refreshes local readiness', async () => {
    const secretExport = '{"localStorage":{"fixture":"private-session-value"}}'
    const importExport = vi.fn().mockResolvedValue([])
    const ready = vi.fn().mockResolvedValueOnce(false).mockResolvedValue(true)
    const refresh = vi.fn().mockResolvedValueOnce('').mockResolvedValue('Сессия обновлена локально.')
    window.go = { main: { App: {
      ImportBrowserExport: importExport,
      HasClientRoomCredentials: ready,
      GetClientCredentialRefreshMessage: refresh,
    } as never } }
    renderSettingsScreen()
    await waitFor(() => expect(ready).toHaveBeenCalledTimes(1))
    const file = new File([secretExport], 'browser-session.json', { type: 'application/json' })
    Object.defineProperty(file, 'text', { value: () => Promise.resolve(secretExport) })
    const input = screen.getByLabelText('Импортировать сессию из браузера')
    fireEvent.change(input, { target: { files: [file] } })

    await waitFor(() => expect(importExport).toHaveBeenCalledWith(secretExport))
    expect(await screen.findByText('Сессия обновлена локально.')).toBeInTheDocument()
    expect(screen.getByText(/Готово: после подключения клиент создаст комнату/i)).toBeInTheDocument()
    expect(input).toHaveValue('')
    expect(document.body).not.toHaveTextContent('private-session-value')
  })

  it('shows a safe import error and permits retry without rendering rejected content', async () => {
    const importExport = vi.fn().mockRejectedValue(new Error('private-session-value'))
    window.go = { main: { App: { ImportBrowserExport: importExport } as never } }
    renderSettingsScreen()
    const file = new File(['{}'], 'browser-session.json', { type: 'application/json' })
    Object.defineProperty(file, 'text', { value: () => Promise.resolve('{}') })
    const input = screen.getByLabelText('Импортировать сессию из браузера')
    fireEvent.change(input, { target: { files: [file] } })

    expect(await screen.findByText('Не удалось импортировать сессию. Выберите JSON-файл экспорта браузера и попробуйте ещё раз.')).toBeInTheDocument()
    expect(input).not.toBeDisabled()
    expect(input).toHaveValue('')
    expect(document.body).not.toHaveTextContent('private-session-value')
  })

  it('does not offer desktop session-file import on Android', () => {
    renderSettingsScreen({ capabilities: { ...baseCapabilities, host: 'capacitor' } })
    expect(screen.queryByLabelText('Импортировать сессию из браузера')).not.toBeInTheDocument()
  })

  it('renders an inspectable smoke-test result with step statuses and routed fields', () => {
    renderSettingsScreen({
      smokeTestResult,
    })

    const result = screen.getByTestId('test-mode-result')
    expect(result).toHaveTextContent('Smoke test failed on node selection')
    expect(result).toHaveTextContent('discover node')
    expect(result).toHaveTextContent('pass')
    expect(result).toHaveTextContent('open tunnel')
    expect(result).toHaveTextContent('fail')
    expect(result).toHaveTextContent('verify egress')
    expect(result).toHaveTextContent('skip')
    expect(result).toHaveTextContent('198.51.100.10')
    expect(result).toHaveTextContent('203.0.113.10')
    expect(result).toHaveTextContent('node-berlin')
    expect(result).toHaveTextContent('/tmp/whitetransport-test-result.json')
  })
})
