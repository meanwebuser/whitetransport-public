import { Brain, Shield, Zap } from 'lucide-react'

export const countryFlags: Record<string, string> = {
  DE: '🇩🇪', NO: '🇳🇴', NL: '🇳🇱', FI: '🇫🇮', JP: '🇯🇵', US: '🇺🇸', UK: '🇬🇧', FR: '🇫🇷',
  SE: '🇸🇪', CH: '🇨🇭', CA: '🇨🇦', SG: '🇸🇬', AU: '🇦🇺', BR: '🇧🇷',
}

export const transportIcons: Record<string, React.ElementType> = {
  auto: Brain,
  ytp: Shield,
  'whitelist-bypass': Zap,
}

export const transportLabels: Record<string, string> = {
  auto: 'Auto',
  ytp: 'YTP',
  'whitelist-bypass': 'WB',
}

export const vpnProtocolNames: Record<string, string> = {
  ytp: 'YTP Tunnel (Stealth)',
  'whitelist-bypass': 'WB Protocol (Fast)',
  auto: 'Auto (YTP+WB)',
}

export function formatBytes(mb: number) {
  if (mb < 1) return `${(mb * 1024).toFixed(0)} KB`
  if (mb < 1024) return `${mb.toFixed(1)} MB`
  return `${(mb / 1024).toFixed(2)} GB`
}

export function formatDuration(seconds: number) {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d >= 1) return `${d} ${d === 1 ? 'day' : 'days'} ${h}h`
  return `${h}h ${m}m`
}

export function getTimeAgo(date: Date): string {
  const now = new Date()
  const diff = Math.floor((now.getTime() - date.getTime()) / 1000)
  if (diff < 60) return 'Just now'
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

export interface HistoryEntry {
  id: string
  server: string
  flag: string
  time: string
  duration: string
  event: string
}

export const qualityConfig: Record<string, { color: string; label: string; percent: number }> = {
  excellent: { color: 'bg-emerald-500', label: 'Excellent', percent: 100 },
  good: { color: 'bg-emerald-400', label: 'Good', percent: 75 },
  fair: { color: 'bg-yellow-500', label: 'Fair', percent: 50 },
  poor: { color: 'bg-red-500', label: 'Poor', percent: 25 },
  disconnected: { color: 'bg-muted-foreground', label: 'Disconnected', percent: 0 },
}
