import { useEffect } from 'react'
import { AppShell } from './components/shell/app-shell'
import { initNativeBridge } from './store/client-store'

export function App() {
  useEffect(() => {
    void initNativeBridge().catch((error: unknown) => {
      console.error('native bridge initialization failed', error)
    })
  }, [])

  return (
    <AppShell />
  )
}
