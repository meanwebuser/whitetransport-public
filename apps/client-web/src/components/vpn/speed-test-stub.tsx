import { Gauge } from 'lucide-react'

// PoC stub for the operator SpeedTest component (apps/admin .../speed-test.tsx,
// 27K). The full speed test isn't part of the connect/proxy PoC; this keeps the
// same prop signature so client-view renders. Wired up properly post-PoC.
interface SpeedTestProps {
  serverId?: string
  clientId?: string
  servers?: { id: string; name: string; countryCode: string }[]
}

export function SpeedTest({ serverId }: SpeedTestProps) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 rounded-lg border border-dashed py-8 text-muted-foreground">
      <Gauge className="h-6 w-6" />
      <p className="text-sm">Speed test {serverId ? `(${serverId})` : ''} — coming soon</p>
    </div>
  )
}
