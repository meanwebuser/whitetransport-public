'use client'

import { useEffect, useState, useCallback } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
import {
  Power,
  Wifi,
  WifiOff,
  Signal,
  Globe,
  ChevronDown,
  ChevronUp,
  Shield,
  ShieldCheck,
  Zap,
  Brain,
  Settings2,
  Activity,
  HardDrive,
  Clock,
  Gauge,
  ArrowDown,
  ArrowUp,
  History,
  Radio,
  Layers,
  Cpu,
  Unplug,
  ArrowRightLeft,
  Star,
  Network,
  Lock,
  RefreshCw,
  Gamepad2,
  MessageCircle,
  Tv,
  AppWindow,
  Globe2,
  Link2,
  Plus,
} from 'lucide-react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { Progress } from '@/components/ui/progress'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { toast } from 'sonner'
import { isCapacitorHosted, isDesktopHosted } from '../../native/wt-transport'
import { DiscoveryPanel } from '../desktop/discovery-panel'
import { LogViewer } from '../desktop/log-viewer'
import { SmokeTestPanel } from '../desktop/smoke-test-panel'
import { StatusDashboard } from '../desktop/status-dashboard'
import {
  useClientStore as useVPNStore,
  type Client,
  type ConnectionLog,
  type Platform,
  type Server,
  type TransportType,
  type TunnelMode,
} from '../../store/client-store'
import { SpeedTest as EnhancedSpeedTest } from './speed-test-stub'
import { useDirectConnectionStore } from '../../store/direct-connection-store'
import { protocolBadge } from '../../lib/parse-direct-uri'
import { whiteTransportToggleLabel } from '../../lib/runtime-accessibility'
import { resolveRuntimeConnectTarget } from '../../lib/runtime-connection'
import { DirectConnectionDialog } from '../desktop/direct-connection-dialog'

const countryFlags: Record<string, string> = {
  DE: '🇩🇪', NO: '🇳🇴', NL: '🇳🇱', FI: '🇫🇮', JP: '🇯🇵', US: '🇺🇸', UK: '🇬🇧', FR: '🇫🇷',
  SE: '🇸🇪', CH: '🇨🇭', CA: '🇨🇦', SG: '🇸🇬', AU: '🇦🇺', BR: '🇧🇷',
}

const transportIcons: Record<string, React.ElementType> = {
  auto: Brain,
  ytp: Shield,
  'whitelist-bypass': Zap,
}

const transportLabels: Record<string, string> = {
  auto: 'Auto',
  ytp: 'YTP',
  'whitelist-bypass': 'WB',
}

const vpnProtocolNames: Record<string, string> = {
  ytp: 'YTP Tunnel (Stealth)',
  'whitelist-bypass': 'WB Protocol (Fast)',
  auto: 'Auto (YTP+WB)',
}

function formatBytes(mb: number) {
  if (mb < 1) return `${(mb * 1024).toFixed(0)} KB`
  if (mb < 1024) return `${mb.toFixed(1)} MB`
  return `${(mb / 1024).toFixed(2)} GB`
}

function formatDuration(seconds: number) {
  if (seconds < 60) return `${seconds}s`
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
  const d = Math.floor(seconds / 86400)
  const h = Math.floor((seconds % 86400) / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (d >= 1) return `${d} ${d === 1 ? 'day' : 'days'} ${h}h`
  return `${h}h ${m}m`
}

// Data usage ring chart
function DataUsageRing({ used, max }: { used: number; max: number }) {
  const percentage = max > 0 ? Math.min((used / max) * 100, 100) : 0
  const radius = 50
  const circumference = 2 * Math.PI * radius
  const offset = circumference - (percentage / 100) * circumference

  return (
    <div className="relative w-36 h-36 mx-auto">
      <svg className="w-full h-full -rotate-90" viewBox="0 0 120 120">
        <defs>
          <linearGradient id="ringGradient" x1="0%" y1="0%" x2="100%" y2="100%">
            <stop offset="0%" stopColor="#10b981" />
            <stop offset="50%" stopColor="#06b6d4" />
            <stop offset="100%" stopColor="#10b981" />
          </linearGradient>
        </defs>
        <circle
          cx="60"
          cy="60"
          r={radius}
          stroke="hsl(var(--muted))"
          strokeWidth="10"
          fill="none"
        />
        <motion.circle
          cx="60"
          cy="60"
          r={radius}
          stroke="url(#ringGradient)"
          strokeWidth="10"
          fill="none"
          strokeLinecap="round"
          strokeDasharray={circumference}
          initial={{ strokeDashoffset: circumference }}
          animate={{ strokeDashoffset: offset }}
          transition={{ duration: 1, ease: 'easeOut' }}
          style={{ filter: 'drop-shadow(0 0 4px rgba(16,185,129,0.5))' }}
        />
      </svg>
      <div className="absolute inset-0 flex flex-col items-center justify-center">
        <span className="text-lg font-bold">{percentage.toFixed(0)}%</span>
        <span className="text-[10px] text-muted-foreground">{formatBytes(used)} / {formatBytes(max)}</span>
      </div>
    </div>
  )
}

// Signal strength bars component
function SignalBars({ ping }: { ping: number }) {
  const bars = 5
  const activeBars = ping < 20 ? 5 : ping < 50 ? 4 : ping < 100 ? 3 : ping < 200 ? 2 : 1
  return (
    <div className="flex items-end gap-0.5 h-4">
      {Array.from({ length: bars }).map((_, i) => (
        <div
          key={i}
          className={`w-1.5 rounded-sm transition-all ${
            i < activeBars
              ? activeBars >= 4
                ? 'bg-emerald-500'
                : activeBars >= 3
                  ? 'bg-yellow-500'
                  : 'bg-red-500'
              : 'bg-muted-foreground/20'
          }`}
          style={{ height: `${(i + 1) * 20}%` }}
        />
      ))}
    </div>
  )
}

// Segmented quality gauge (5 segments)
function SegmentedGauge({ quality }: { quality: string }) {
  const qualityMap: Record<string, { filled: number; color: string; label: string }> = {
    excellent: { filled: 5, color: 'bg-emerald-500', label: 'Excellent' },
    good: { filled: 4, color: 'bg-emerald-400', label: 'Good' },
    fair: { filled: 3, color: 'bg-yellow-500', label: 'Fair' },
    poor: { filled: 2, color: 'bg-red-500', label: 'Poor' },
    disconnected: { filled: 0, color: 'bg-muted-foreground', label: 'Disconnected' },
  }
  const config = qualityMap[quality] || qualityMap.disconnected

  return (
    <div className="w-full max-w-xs">
      <div className="relative">
        <div className="flex gap-1 h-8">
          {Array.from({ length: 5 }).map((_, i) => (
            <div
              key={i}
              className={`flex-1 rounded-sm transition-all duration-500 ${
                i < config.filled ? config.color : 'bg-muted-foreground/15'
              }`}
            />
          ))}
        </div>
        {/* Centered overlay label */}
        <div className="absolute inset-0 flex items-center justify-center">
          <span className="text-xs font-bold text-white drop-shadow-md">{config.label}</span>
        </div>
      </div>
    </div>
  )
}

// Speed test component
function SpeedTest({ onDone }: { onDone: () => void }) {
  const [phase, setPhase] = useState<'download' | 'upload' | 'done'>('download')
  const [downloadSpeed, setDownloadSpeed] = useState(0)
  const [uploadSpeed, setUploadSpeed] = useState(0)
  const [progress, setProgress] = useState(0)

  useEffect(() => {
    if (phase === 'download') {
      const targetSpeed = Math.floor(Math.random() * 80) + 40
      let current = 0
      const interval = setInterval(() => {
        current += Math.random() * 15
        if (current >= targetSpeed) {
          current = targetSpeed
          setDownloadSpeed(targetSpeed)
          setProgress(100)
          setTimeout(() => {
            setPhase('upload')
            setProgress(0)
          }, 500)
          clearInterval(interval)
        } else {
          setDownloadSpeed(Math.floor(current))
          setProgress(Math.floor((current / targetSpeed) * 100))
        }
      }, 200)
      return () => clearInterval(interval)
    }

    if (phase === 'upload') {
      const targetSpeed = Math.floor(Math.random() * 40) + 15
      let current = 0
      const interval = setInterval(() => {
        current += Math.random() * 10
        if (current >= targetSpeed) {
          current = targetSpeed
          setUploadSpeed(targetSpeed)
          setProgress(100)
          setTimeout(() => {
            setPhase('done')
            onDone()
          }, 500)
          clearInterval(interval)
        } else {
          setUploadSpeed(Math.floor(current))
          setProgress(Math.floor((current / targetSpeed) * 100))
        }
      }, 200)
      return () => clearInterval(interval)
    }
  }, [phase, onDone])

  return (
    <motion.div
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: -10 }}
      className="space-y-4"
    >
      <div className="flex items-center gap-2">
        <Gauge className="h-4 w-4 text-amber-500" />
        <span className="text-sm font-semibold">Speed Test</span>
      </div>

      {phase !== 'done' ? (
        <div className="space-y-3">
          <div className="flex items-center justify-between">
            <span className="text-xs text-muted-foreground">
              {phase === 'download' ? 'Testing Download...' : 'Testing Upload...'}
            </span>
            <span className="text-xs text-muted-foreground">{progress}%</span>
          </div>
          <Progress value={progress} className="h-2" />

          <div className="grid grid-cols-2 gap-3">
            <div className="p-3 rounded-lg bg-muted/50 text-center">
              <ArrowDown className="h-4 w-4 text-emerald-500 mx-auto mb-1" />
              <p className="text-xs text-muted-foreground">Download</p>
              <p className="text-lg font-bold text-emerald-400">
                {downloadSpeed} <span className="text-xs font-normal">Mbps</span>
              </p>
            </div>
            <div className="p-3 rounded-lg bg-muted/50 text-center">
              <ArrowUp className="h-4 w-4 text-cyan-500 mx-auto mb-1" />
              <p className="text-xs text-muted-foreground">Upload</p>
              <p className="text-lg font-bold text-cyan-400">
                {uploadSpeed} <span className="text-xs font-normal">Mbps</span>
              </p>
            </div>
          </div>
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-3">
          <div className="p-3 rounded-lg bg-emerald-500/10 text-center">
            <ArrowDown className="h-4 w-4 text-emerald-500 mx-auto mb-1" />
            <p className="text-xs text-muted-foreground">Download</p>
            <p className="text-lg font-bold text-emerald-400">
              {downloadSpeed} <span className="text-xs font-normal">Mbps</span>
            </p>
          </div>
          <div className="p-3 rounded-lg bg-cyan-500/10 text-center">
            <ArrowUp className="h-4 w-4 text-cyan-500 mx-auto mb-1" />
            <p className="text-xs text-muted-foreground">Upload</p>
            <p className="text-lg font-bold text-cyan-400">
              {uploadSpeed} <span className="text-xs font-normal">Mbps</span>
            </p>
          </div>
        </div>
      )}
    </motion.div>
  )
}

interface HistoryEntry {
  id: string
  server: string
  flag: string
  time: string
  duration: string
  event: string
}

export function ClientView() {
  const {
    servers,
    clients,
    logs,
    runtimeNodes,
    carrierHealth,
    desktopStatus,
    daemonLogs,
    smokeTest,
    fetchServers,
    fetchClients,
    fetchLogs,
    connectClient,
    disconnectClient,
    updateClient,
    setActiveTab,
    refreshDesktopTelemetry,
    runDesktopSmokeTest,
    restartRuntime,
  } = useVPNStore()

  const [selectedClientId, setSelectedClientId] = useState<string>('')
  const [showAdvanced, setShowAdvanced] = useState(false)
  const [isConnecting, setIsConnecting] = useState(false)
  const [selectedServerId, setSelectedServerId] = useState<string>('')
  const [showSpeedTest, setShowSpeedTest] = useState(false)
  const [elapsedSeconds, setElapsedSeconds] = useState(0)
  const [killSwitch, setKillSwitch] = useState(true)
  const [dnsLeakProtection, setDnsLeakProtection] = useState(true)
  const [splitTunneling, setSplitTunneling] = useState(false)
  const [splitTunnelApps, setSplitTunnelApps] = useState<Record<string, boolean>>({
    browser: true,
    messaging: true,
    streaming: false,
    gaming: false,
    other: true,
  })
const [dnsProvider, setDnsProvider] = useState('ultravpn')
const [customDns, setCustomDns] = useState('')
const [directDialogOpen, setDirectDialogOpen] = useState(false)
const directConnections = useDirectConnectionStore((s) => s.connections)
const isDesktopHost = isDesktopHosted()
const isCapacitorHost = isCapacitorHosted()
const activeSelectedClientId = selectedClientId || clients[0]?.id || ''

  useEffect(() => {
    fetchServers()
    fetchClients()
    fetchLogs()
  }, [fetchServers, fetchClients, fetchLogs])

  useEffect(() => {
    if (!isDesktopHost) return
    refreshDesktopTelemetry().catch(() => undefined)
  }, [isDesktopHost, refreshDesktopTelemetry])

  const connectionHistory: HistoryEntry[] = (() => {
    if (!activeSelectedClientId || logs.length === 0) return []
    const clientLogs = logs
      .filter((l: ConnectionLog) => l.clientId === activeSelectedClientId && (l.event === 'connect' || l.event === 'disconnect'))
      .slice(0, 5)
    return clientLogs.map((l: ConnectionLog) => {
      const server = l.server || servers.find((s: Server) => s.id === l.serverId)
      const serverName = server?.name || 'Unknown'
      const serverCountry = server?.countryCode || ''
      const flag = countryFlags[serverCountry] || '🌐'
      const timeAgo = getTimeAgo(new Date(l.createdAt))
      const duration = l.duration ? String(l.duration) : (l.event === 'disconnect' ? '—' : 'Active')
      return {
        id: l.id,
        server: serverName,
        flag,
        time: timeAgo,
        duration,
        event: l.event,
      }
    })
  })()

  function getTimeAgo(date: Date): string {
    const now = new Date()
    const diff = Math.floor((now.getTime() - date.getTime()) / 1000)
    if (diff < 60) return 'Just now'
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
    return `${Math.floor(diff / 86400)}d ago`
  }

  const currentClient = clients.find((c: Client) => c.id === activeSelectedClientId)
  const connectedServer = currentClient?.connectedServerId
    ? servers.find((s: Server) => s.id === currentClient.connectedServerId) || currentClient.connectedServer
    : currentClient?.connectedServer || null

  const isOnline = currentClient?.status === 'online'

  useEffect(() => {
    const initialElapsedSeconds = isOnline ? currentClient?.sessionDuration || 0 : 0
    const initialTimer = setTimeout(() => setElapsedSeconds(initialElapsedSeconds), 0)

    if (isOnline) {
      const interval = setInterval(() => {
        setElapsedSeconds((prev) => prev + 1)
      }, 1000)
      return () => {
        clearTimeout(initialTimer)
        clearInterval(interval)
      }
    }

    return () => clearTimeout(initialTimer)
  }, [isOnline, currentClient?.sessionDuration])

  const handleToggleConnection = useCallback(async () => {
    if (!currentClient) return
    setIsConnecting(true)

    try {
      if (isOnline) {
        await disconnectClient(currentClient.id)
        toast.success('Disconnected', { description: `Disconnected from ${connectedServer?.name || 'server'}` })
      } else {
        const onlineServer = servers.find((server: Server) => server.status === 'online')
        const target = resolveRuntimeConnectTarget({
          selectedServerId,
          preferredServerId: currentClient.preferredServerId,
          onlineServerId: onlineServer?.id,
          knownServerIds: servers.map((server: Server) => server.id),
          capacitorHost: isCapacitorHost,
        })
        if (target.kind === 'server') {
          await connectClient(currentClient.id, target.serverId)
          const server = servers.find((candidate: Server) => candidate.id === target.serverId)
          toast.success('Connected', { description: `Connected to ${server?.name || 'server'}` })
        } else if (target.kind === 'runtime') {
          // Capacitor's bridge starts gomobile discovery and chooses a node.
          await connectClient(currentClient.id, undefined)
          toast.success('Connected', { description: 'Connected through the native runtime' })
        } else {
          toast.error('No nodes available', { description: 'No reachable nodes discovered. Ensure the daemon is running and discovery is configured.' })
        }
      }
    } catch (err) {
      const msg = err instanceof Error ? err.message : String(err)
      toast.error('Connection failed', { description: msg })
    } finally {
      setTimeout(() => setIsConnecting(false), 1000)
    }
  }, [currentClient, isOnline, selectedServerId, connectedServer, servers, connectClient, disconnectClient, isCapacitorHost])

  const handleServerSwitch = async (serverId: string) => {
    if (!currentClient) return
    setSelectedServerId(serverId)
    if (isOnline) {
      await disconnectClient(currentClient.id)
      await connectClient(currentClient.id, serverId)
      const server = servers.find((s: Server) => s.id === serverId)
      toast.success('Server Switched', { description: `Now connected to ${server?.name}` })
    } else {
      await connectClient(currentClient.id, serverId)
      const server = servers.find((s: Server) => s.id === serverId)
      toast.success('Connected', { description: `Connected to ${server?.name}` })
    }
  }

  const handleUpdateSetting = async (field: string, value: unknown) => {
    if (!currentClient) return
    await updateClient(currentClient.id, { [field]: value })
  }

  const onlineServers = servers.filter((s: Server) => s.status === 'online')

  const connectionQuality = connectedServer
    ? connectedServer.ping < 50
      ? 'excellent'
      : connectedServer.ping < 100
        ? 'good'
        : connectedServer.ping < 200
          ? 'fair'
          : 'poor'
    : 'disconnected'

  const qualityConfig: Record<string, { color: string; label: string; percent: number }> = {
    excellent: { color: 'bg-emerald-500', label: 'Excellent', percent: 100 },
    good: { color: 'bg-emerald-400', label: 'Good', percent: 75 },
    fair: { color: 'bg-yellow-500', label: 'Fair', percent: 50 },
    poor: { color: 'bg-red-500', label: 'Poor', percent: 25 },
    disconnected: { color: 'bg-muted-foreground', label: 'Disconnected', percent: 0 },
  }

  const TransportIcon = connectedServer
    ? transportIcons[connectedServer.transportType] || Radio
    : null

  // Bandwidth values for current client
  const bandwidthLimit = currentClient?.bandwidthLimit || 5120
  const bandwidthUsed = currentClient?.bandwidthUsed || currentClient?.totalDataUsed || 0
  const downloadSpeed = currentClient?.downloadSpeed || 0
  const uploadSpeed = currentClient?.uploadSpeed || 0

  // "Connected since" timestamp
  const connectedSince = isOnline && currentClient?.sessionDuration
    ? new Date(Date.now() - currentClient.sessionDuration * 1000)
    : null

  if (clients.length === 0) {
    return (
      <div className="flex items-center justify-center min-h-[60vh]">
        <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border max-w-md w-full">
          <CardContent className="p-8 text-center">
            <WifiOff className="h-12 w-12 text-muted-foreground mx-auto mb-4" />
            <h3 className="text-lg font-semibold mb-2">No Client Configured</h3>
            <p className="text-sm text-muted-foreground">
              Ask your admin to add a client device for you, or switch to Admin view to set one up.
            </p>
          </CardContent>
        </Card>
      </div>
    )
  }

  const primaryView = (
    <TooltipProvider>
      <motion.div
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        className="max-w-lg mx-auto space-y-4"
      >
        {/* Client Selector (if multiple clients) */}
        {clients.length > 1 && (
          <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
            <CardContent className="p-3">
              <div className="flex items-center gap-2">
                <Select value={activeSelectedClientId} onValueChange={setSelectedClientId}>
                  <SelectTrigger className="h-11 flex-1">
                    <SelectValue placeholder="Select device" />
                  </SelectTrigger>
                  <SelectContent>
                    {clients.map((client: Client) => (
                      <SelectItem key={client.id} value={client.id}>
                        {client.name} ({client.deviceType})
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Badge className={`shrink-0 text-[10px] px-2 py-1 h-6 ${
                  isOnline
                    ? 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30'
                    : 'bg-muted/50 text-muted-foreground border-muted-foreground/20'
                }`}>
                  <span className={`h-1.5 w-1.5 rounded-full mr-1.5 ${isOnline ? 'bg-emerald-400 animate-pulse' : 'bg-muted-foreground'}`} />
                  {isOnline ? 'Connected' : 'Disconnected'}
                </Badge>
              </div>
            </CardContent>
          </Card>
        )}

        {/* Connection Status Card */}
        <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border overflow-hidden relative">
          <div className="absolute inset-0 pointer-events-none" style={{ background: 'radial-gradient(circle at 50% 40%, rgba(16,185,129,0.08) 0%, transparent 60%)' }} />
          <CardContent className="p-6 pt-8 relative">
            <div className="flex flex-col items-center text-center space-y-4">
              {/* Smaller Connect Button (w-28 h-28) */}
              <motion.div
                className="relative"
                animate={isOnline ? { scale: [1, 1.02, 1] } : {}}
                transition={{ repeat: Infinity, duration: 2 }}
              >
                {isOnline && (
                  <motion.div
                    className="absolute inset-0 rounded-full bg-emerald-500/20 blur-xl"
                    animate={{ scale: [1, 1.3, 1], opacity: [0.3, 0.6, 0.3] }}
                    transition={{ repeat: Infinity, duration: 2 }}
                  />
                )}

                {isOnline && (
                  <>
                    <motion.div
                      className="absolute inset-0 rounded-full border-2 border-emerald-500/40"
                      animate={{ scale: [1, 1.5], opacity: [0.6, 0] }}
                      transition={{ repeat: Infinity, duration: 2, delay: 0 }}
                    />
                    <motion.div
                      className="absolute inset-0 rounded-full border-2 border-emerald-500/30"
                      animate={{ scale: [1, 1.5], opacity: [0.4, 0] }}
                      transition={{ repeat: Infinity, duration: 2, delay: 0.7 }}
                    />
                    <motion.div
                      className="absolute inset-0 rounded-full border border-emerald-500/20"
                      animate={{ scale: [1, 1.5], opacity: [0.3, 0] }}
                      transition={{ repeat: Infinity, duration: 2, delay: 1.3 }}
                    />
                  </>
                )}

                <motion.button
                  data-testid="wt-connect-toggle"
                  aria-label={whiteTransportToggleLabel(isOnline)}
                  className={`relative w-28 h-28 rounded-full flex items-center justify-center transition-all duration-500 ${
                    isOnline
                      ? 'bg-gradient-to-br from-emerald-500 to-emerald-600 shadow-[0_0_40px_rgba(16,185,129,0.3)]'
                      : isConnecting
                        ? 'bg-muted hover:bg-emerald-500/20 ring-2 ring-muted-foreground/20'
                        : 'bg-muted hover:bg-emerald-500/20 ring-2 ring-muted-foreground/20'
                  }`}
                  onClick={handleToggleConnection}
                  disabled={isConnecting}
                  whileTap={{ scale: 0.95 }}
                >
                  {isConnecting && (
                    <motion.div
                      className="absolute inset-0 rounded-full"
                      style={{
                        background: 'conic-gradient(from 0deg, #10b981, #06b6d4, #f59e0b, #10b981)',
                      }}
                      animate={{ rotate: 360 }}
                      transition={{ repeat: Infinity, duration: 2, ease: 'linear' }}
                    />
                  )}
                  {isConnecting && (
                    <div className="absolute inset-[3px] rounded-full bg-card" />
                  )}
                  {isConnecting ? (
                    <motion.div
                      animate={{ rotate: 360 }}
                      transition={{ repeat: Infinity, duration: 1, ease: 'linear' }}
                      className="relative z-10"
                    >
                      <Activity className="h-8 w-8 text-emerald-400" />
                    </motion.div>
                  ) : isOnline ? (
                    <Power className="h-8 w-8 text-white relative z-10" />
                  ) : (
                    <Power className="h-8 w-8 text-muted-foreground relative z-10" />
                  )}
                </motion.button>
              </motion.div>

              {/* Status Text - with prominent duration */}
              <div>
                <AnimatePresence mode="wait">
                  <motion.p
                    data-testid="wt-status"
                    key={isOnline ? 'online' : 'offline'}
                    initial={{ opacity: 0, y: 10 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={{ opacity: 0, y: -10 }}
                    className={`text-sm font-medium ${isOnline ? 'text-emerald-400/80 animate-pulse' : 'text-muted-foreground'}`}
                  >
                    {isConnecting ? 'Connecting...' : isOnline ? 'Connected' : 'Disconnected'}
                  </motion.p>
                </AnimatePresence>

                {/* Duration - Session Timer with LIVE badge */}
                {isOnline && elapsedSeconds > 0 && (
                  <div className="flex items-center justify-center gap-2 mt-1">
                    <Badge className="animate-live-badge bg-emerald-500 text-white text-[9px] h-5 px-1.5 font-bold tracking-wider">
                      LIVE
                    </Badge>
                    <motion.p
                      initial={{ opacity: 0 }}
                      animate={{ opacity: 1 }}
                      className="text-lg font-bold text-emerald-400"
                      style={{ textShadow: '0 0 20px rgba(16,185,129,0.5)' }}
                    >
                      {formatDuration(elapsedSeconds)}
                    </motion.p>
                  </div>
                )}

                {/* Connected since timestamp */}
                {isOnline && connectedSince && (
                  <motion.div
                    initial={{ opacity: 0 }}
                    animate={{ opacity: 1 }}
                    className="flex items-center justify-center gap-1.5 mt-1"
                  >
                    <Clock className="h-3 w-3 text-muted-foreground" />
                    <span className="text-xs text-muted-foreground">
                      Since: {connectedSince.toLocaleDateString('en-US', { month: 'short', day: 'numeric' })}, {connectedSince.toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit' })}
                    </span>
                  </motion.div>
                )}
              </div>

              {/* Segmented Quality Gauge */}
              <div className="w-full max-w-xs">
                <div className="flex items-center justify-between mb-1.5">
                  <span className="text-xs text-muted-foreground">Connection Quality</span>
                </div>
                <SegmentedGauge quality={connectionQuality} />
              </div>
            </div>
          </CardContent>
        </Card>

        {/* Server Info Card - Connected Server with data flow visualization */}
        {isOnline && connectedServer && (
          <motion.div
            initial={{ opacity: 0, y: 10 }}
            animate={{ opacity: 1, y: 0 }}
          >
            <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-emerald-500 relative overflow-hidden">
              {/* Animated data flow visualization */}
              <div className="absolute inset-x-0 top-0 h-1 overflow-hidden">
                {[...Array(5)].map((_, i) => (
                  <motion.div
                    key={i}
                    className="absolute h-1 w-3 rounded-full bg-emerald-400/60"
                    style={{ top: 0 }}
                    animate={{
                      left: ['-10%', '110%'],
                      opacity: [0, 1, 1, 0],
                    }}
                    transition={{
                      repeat: Infinity,
                      duration: 2.5,
                      delay: i * 0.5,
                      ease: 'linear',
                    }}
                  />
                ))}
              </div>
              <CardContent className="p-4">
                <div className="flex items-center gap-3">
                  <span className="text-3xl">{countryFlags[connectedServer.countryCode] || '🌐'}</span>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-semibold truncate">{connectedServer.name}</p>
                    <p className="text-xs text-muted-foreground">{connectedServer.city || connectedServer.country} · {connectedServer.ping}ms</p>
                  </div>
                  <div className="flex items-center gap-2">
                    {/* Transport indicator with appropriate icon */}
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Badge variant="outline" className={`text-[10px] px-1.5 py-0.5 h-5 flex items-center gap-1 ${
                          connectedServer.transportType === 'ytp' ? 'border-emerald-500/40 text-emerald-400 bg-emerald-500/10' : connectedServer.transportType === 'whitelist-bypass' ? 'border-cyan-500/40 text-cyan-400 bg-cyan-500/10' : 'border-amber-500/40 text-amber-400 bg-amber-500/10'
                        }`}>
                          {TransportIcon && <TransportIcon className="h-3 w-3" />}
                          {transportLabels[connectedServer.transportType] || 'Auto'}
                        </Badge>
                      </TooltipTrigger>
                      <TooltipContent>
                        {connectedServer.transportType === 'ytp' ? 'YTP: Slow but stable tunnel' : connectedServer.transportType === 'whitelist-bypass' ? 'WB: Fast protocol' : 'Auto: Smart switching'}
                      </TooltipContent>
                    </Tooltip>
                    {/* Quick Reconnect Button */}
                    <Tooltip>
                      <TooltipTrigger asChild>
                        <Button
                          variant="ghost"
                          size="icon"
                          className="h-7 w-7 rounded-full hover:bg-emerald-500/10"
                          onClick={async () => {
                            if (!currentClient?.connectedServerId) return
                            await disconnectClient(currentClient.id)
                            await connectClient(currentClient.id, currentClient.connectedServerId)
                            toast.success('Reconnected', { description: `Reconnected to ${connectedServer.name}` })
                          }}
                          disabled={isConnecting}
                        >
                          <RefreshCw className="h-3.5 w-3.5 text-emerald-400" />
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent>Quick Reconnect</TooltipContent>
                    </Tooltip>
                  </div>
                </div>
                <div className="flex items-center gap-2 mt-2 pt-2 border-t border-border/50">
                  <Network className="h-3.5 w-3.5 text-emerald-500" />
                  <span className="text-xs font-mono text-emerald-400">{connectedServer?.name ?? 'Connected'}</span>
                  <span className="text-xs text-muted-foreground">· SOCKS5 :{currentClient?.socksPort ?? 8809}</span>
                </div>
              </CardContent>
            </Card>
          </motion.div>
        )}

        {/* Security Info Panel */}
        {isOnline && connectedServer && (
          <motion.div
            initial={{ opacity: 0, y: 10 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ delay: 0.1 }}
          >
            <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border overflow-hidden relative">
              <div className="absolute inset-0 pointer-events-none" style={{ background: 'linear-gradient(135deg, rgba(16,185,129,0.04) 0%, rgba(6,182,212,0.03) 50%, transparent 100%)' }} />
              <CardHeader className="pb-3 pt-4 px-4 relative">
                <CardTitle className="text-sm font-semibold flex items-center gap-2">
                  <ShieldCheck className="h-4 w-4 text-emerald-500" />
                  Security Info
                </CardTitle>
              </CardHeader>
              <CardContent className="px-4 pb-4 relative">
                <div className="grid grid-cols-2 gap-2.5">
                  {/* Encryption Protocol */}
                  <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
                    <div className="p-1.5 rounded-full bg-emerald-500/10 shrink-0">
                      <Lock className="h-3.5 w-3.5 text-emerald-400" />
                    </div>
                    <div className="min-w-0">
                      <p className="text-[10px] text-muted-foreground">Encryption</p>
                      <Badge className="bg-emerald-500/15 text-emerald-400 border-emerald-500/30 text-[9px] h-4 px-1.5 mt-0.5">AES-256-GCM</Badge>
                    </div>
                  </div>

                  {/* DNS Leak Protection */}
                  <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
                    <div className={`p-1.5 rounded-full shrink-0 ${dnsLeakProtection ? 'bg-emerald-500/10' : 'bg-red-500/10'}`}>
                      <ShieldCheck className={`h-3.5 w-3.5 ${dnsLeakProtection ? 'text-emerald-400' : 'text-red-400'}`} />
                    </div>
                    <div className="min-w-0">
                      <p className="text-[10px] text-muted-foreground">DNS Leak</p>
                      <Badge className={`text-[9px] h-4 px-1.5 mt-0.5 ${
                        dnsLeakProtection
                          ? 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30'
                          : 'bg-red-500/15 text-red-400 border-red-500/30'
                      }`}>
                        {dnsLeakProtection ? 'Protected' : 'Exposed'}
                      </Badge>
                    </div>
                  </div>

                  {/* Kill Switch */}
                  <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
                    <div className={`p-1.5 rounded-full shrink-0 ${killSwitch ? 'bg-emerald-500/10' : 'bg-red-500/10'}`}>
                      <Power className={`h-3.5 w-3.5 ${killSwitch ? 'text-emerald-400' : 'text-red-400'}`} />
                    </div>
                    <div className="min-w-0">
                      <p className="text-[10px] text-muted-foreground">Kill Switch</p>
                      <Badge className={`text-[9px] h-4 px-1.5 mt-0.5 ${
                        killSwitch
                          ? 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30'
                          : 'bg-red-500/15 text-red-400 border-red-500/30'
                      }`}>
                        {killSwitch ? 'Active' : 'Inactive'}
                      </Badge>
                    </div>
                  </div>

                  {/* VPN Protocol */}
                  <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
                    <div className="p-1.5 rounded-full bg-cyan-500/10 shrink-0">
                      <Radio className="h-3.5 w-3.5 text-cyan-400" />
                    </div>
                    <div className="min-w-0">
                      <p className="text-[10px] text-muted-foreground">Protocol</p>
                      <Badge className="bg-cyan-500/15 text-cyan-400 border-cyan-500/30 text-[9px] h-4 px-1.5 mt-0.5 truncate max-w-full">
                        {vpnProtocolNames[connectedServer.transportType] || 'Auto (YTP+WB)'}
                      </Badge>
                    </div>
                  </div>

                  {/* Split Tunneling */}
                  <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
                    <div className={`p-1.5 rounded-full shrink-0 ${splitTunneling ? 'bg-amber-500/10' : 'bg-emerald-500/10'}`}>
                      <ArrowRightLeft className={`h-3.5 w-3.5 ${splitTunneling ? 'text-amber-400' : 'text-emerald-400'}`} />
                    </div>
                    <div className="min-w-0">
                      <p className="text-[10px] text-muted-foreground">Split Tunnel</p>
                      <Badge className={`text-[9px] h-4 px-1.5 mt-0.5 ${
                        splitTunneling
                          ? 'bg-amber-500/15 text-amber-400 border-amber-500/30'
                          : 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30'
                      }`}>
                        {splitTunneling ? 'Custom rules' : 'All traffic'}
                      </Badge>
                    </div>
                  </div>

                  {/* WebRTC Leak */}
                  <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
                    <div className="p-1.5 rounded-full bg-emerald-500/10 shrink-0">
                      <Globe2 className="h-3.5 w-3.5 text-emerald-400" />
                    </div>
                    <div className="min-w-0">
                      <p className="text-[10px] text-muted-foreground">WebRTC Leak</p>
                      <Badge className="bg-emerald-500/15 text-emerald-400 border-emerald-500/30 text-[9px] h-4 px-1.5 mt-0.5">
                        Protected
                      </Badge>
                    </div>
                  </div>
                </div>
              </CardContent>
            </Card>
          </motion.div>
        )}

        {/* Quick Action Buttons */}
        <motion.div
          initial={{ opacity: 0, y: 10 }}
          animate={{ opacity: 1, y: 0 }}
          className="grid grid-cols-5 gap-2"
        >
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                data-testid="wt-connect-toggle-quick"
                variant="outline"
                size="sm"
                className={`h-auto py-2.5 flex-col gap-1 ${isOnline ? 'border-red-500/20 text-red-400 hover:bg-red-500/10 hover:text-red-300 ripple-effect' : 'border-emerald-500/20 text-emerald-400 hover:bg-emerald-500/10 hover:text-emerald-300'}`}
                onClick={handleToggleConnection}
                disabled={isConnecting}
              >
                <Unplug className="h-4 w-4" />
                <span className="text-[10px] font-medium">{isOnline ? 'Disconnect' : 'Connect'}</span>
              </Button>
            </TooltipTrigger>
            <TooltipContent>{isOnline ? 'Disconnect from current server' : 'Connect to VPN'}</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                className="h-auto py-2.5 flex-col gap-1 border-cyan-500/20 text-cyan-400 hover:bg-cyan-500/10 hover:text-cyan-300"
                onClick={() => {
                  if (setActiveTab) setActiveTab('servers')
                }}
              >
                <ArrowRightLeft className="h-4 w-4" />
                <span className="text-[10px] font-medium">Switch</span>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Switch to a different server</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                className="h-auto py-2.5 flex-col gap-1 border-amber-500/20 text-amber-400 hover:bg-amber-500/10 hover:text-amber-300"
                onClick={() => setShowSpeedTest(true)}
                disabled={!isOnline}
              >
                <Gauge className="h-4 w-4" />
                <span className="text-[10px] font-medium">Speed Test</span>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Run a speed test</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                className="h-auto py-2.5 flex-col gap-1 border-emerald-500/20 text-emerald-400 hover:bg-emerald-500/10 hover:text-emerald-300"
                onClick={() => setDirectDialogOpen(true)}
              >
                <Plus className="h-4 w-4" />
                <span className="text-[10px] font-medium">Direct</span>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Add direct connection</TooltipContent>
          </Tooltip>
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                variant="outline"
                size="sm"
                className="h-auto py-2.5 flex-col gap-1 border-teal-500/20 text-teal-400 hover:bg-teal-500/10 hover:text-teal-300"
                onClick={() => setShowAdvanced(!showAdvanced)}
              >
                <Settings2 className="h-4 w-4" />
                <span className="text-[10px] font-medium">Settings</span>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Open advanced settings</TooltipContent>
          </Tooltip>
        </motion.div>

        {/* Stats Row - 4 cards with colored left-border accents */}
        {isOnline && currentClient && (
          <motion.div
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            className="grid grid-cols-2 gap-3"
          >
            <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-emerald-500 hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300">
              <CardContent className="p-3 flex items-center gap-3">
                <div className="p-2 rounded-full bg-emerald-500/10 shrink-0">
                  <ArrowDown className="h-4 w-4 text-emerald-500" />
                </div>
                <div className="min-w-0">
                  <p className="text-xs uppercase tracking-widest text-muted-foreground">Download</p>
                  <p className="text-2xl font-bold">{downloadSpeed} <span className="text-xs font-normal text-muted-foreground">Mbps</span></p>
                </div>
              </CardContent>
            </Card>
            <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-cyan-500 hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300">
              <CardContent className="p-3 flex items-center gap-3">
                <div className="p-2 rounded-full bg-cyan-500/10 shrink-0">
                  <ArrowUp className="h-4 w-4 text-cyan-500" />
                </div>
                <div className="min-w-0">
                  <p className="text-xs uppercase tracking-widest text-muted-foreground">Upload</p>
                  <p className="text-2xl font-bold">{uploadSpeed} <span className="text-xs font-normal text-muted-foreground">Mbps</span></p>
                </div>
              </CardContent>
            </Card>
            <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-amber-500 hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300">
              <CardContent className="p-3 flex items-center gap-3">
                <div className="p-2 rounded-full bg-amber-500/10 shrink-0">
                  <Signal className="h-4 w-4 text-amber-500" />
                </div>
                <div className="min-w-0">
                  <p className="text-xs uppercase tracking-widest text-muted-foreground">Ping</p>
                  <p className="text-2xl font-bold">{connectedServer?.ping || 0}<span className="text-xs font-normal text-muted-foreground">ms</span></p>
                </div>
              </CardContent>
            </Card>
            <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-teal-500 hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300">
              <CardContent className="p-3 flex items-center gap-3">
                <div className="p-2 rounded-full bg-teal-500/10 shrink-0">
                  <HardDrive className="h-4 w-4 text-teal-500" />
                </div>
                <div className="min-w-0">
                  <p className="text-xs uppercase tracking-widest text-muted-foreground">Data</p>
                  <p className="text-2xl font-bold">{formatBytes(currentClient.totalDataUsed)}</p>
                </div>
              </CardContent>
            </Card>
          </motion.div>
        )}

        {/* Data Usage Ring & Speed Test */}
        {currentClient && (
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
            {/* Data Usage Ring */}
            <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
              <CardContent className="p-4">
                <div className="flex items-center gap-2 mb-3">
                  <HardDrive className="h-4 w-4 text-cyan-500" />
                  <span className="text-sm font-semibold">Session Data</span>
                </div>
                <DataUsageRing used={bandwidthUsed} max={bandwidthLimit} />
                <div className="flex justify-between text-xs text-muted-foreground mt-3">
                  <span>{formatBytes(bandwidthUsed)} used</span>
                  <span>{formatBytes(bandwidthLimit)} limit</span>
                </div>
                <div className="grid grid-cols-2 gap-2 mt-3">
                  <div className="p-2 rounded-lg bg-emerald-500/10 text-center">
                    <ArrowDown className="h-3 w-3 text-emerald-500 mx-auto" />
                    <p className="text-xs font-bold text-emerald-400">{downloadSpeed} Mbps</p>
                    <p className="text-[10px] text-muted-foreground">Download</p>
                  </div>
                  <div className="p-2 rounded-lg bg-cyan-500/10 text-center">
                    <ArrowUp className="h-3 w-3 text-cyan-500 mx-auto" />
                    <p className="text-xs font-bold text-cyan-400">{uploadSpeed} Mbps</p>
                    <p className="text-[10px] text-muted-foreground">Upload</p>
                  </div>
                </div>
              </CardContent>
            </Card>

            {/* Speed Test - Enhanced */}
            <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
              <CardContent className="p-4">
                {!showSpeedTest ? (
                  <div className="flex flex-col items-center justify-center h-full py-4">
                    <Gauge className="h-8 w-8 text-amber-500 mb-3" />
                    <p className="text-sm font-medium mb-2">Speed Test</p>
                    <p className="text-xs text-muted-foreground mb-4">Test your connection speed</p>
                    <Button
                      size="sm"
                      onClick={() => setShowSpeedTest(true)}
                      disabled={!isOnline}
                      className="bg-amber-600 hover:bg-amber-700 text-white"
                    >
                      Run Test
                    </Button>
                  </div>
                ) : (
                  <EnhancedSpeedTest
                    serverId={connectedServer?.id || currentClient?.connectedServerId || undefined}
                    clientId={currentClient?.id}
              servers={servers.map((s: Server) => ({ id: s.id, name: s.name, countryCode: s.countryCode }))}
                  />
                )}
                {showSpeedTest && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="mt-2 w-full text-xs"
                    onClick={() => setShowSpeedTest(false)}
                  >
                    Close
                  </Button>
                )}
              </CardContent>
            </Card>
          </div>
        )}

        {/* Connection History */}
        <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
          <CardHeader className="pb-3">
            <CardTitle className="text-base font-semibold flex items-center gap-2">
              <History className="h-4 w-4 text-emerald-500" />
              Connection History
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ScrollArea className="max-h-40">
              <div className="space-y-2" data-testid="wt-node-list">
                {connectionHistory.length > 0 ? connectionHistory.map((entry) => (
                  <div
                    key={entry.id}
                    className={`flex items-center justify-between py-2 px-3 rounded-lg bg-muted/30 hover:bg-muted/50 transition-all border-l-4 ${entry.event === 'connect' ? 'border-l-emerald-500' : 'border-l-red-500'}`}
                  >
                    <div className="flex items-center gap-2">
                      <span className="text-lg">{entry.flag}</span>
                      <div>
                        <p className="text-sm font-medium">{entry.server}</p>
                        <p className="text-xs font-mono text-muted-foreground">{entry.time}</p>
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      <Badge variant={entry.event === 'connect' ? 'default' : 'outline'} className={`text-xs ${entry.event === 'connect' ? 'bg-emerald-500/20 text-emerald-400 border-emerald-500/30' : 'bg-red-500/15 text-red-400 border-red-500/30'}`}>
                        {entry.event === 'connect' ? '●' : '○'}
                      </Badge>
                      <Badge variant="outline" className="text-xs border-emerald-500/30 text-emerald-400">
                        {entry.duration}
                      </Badge>
                    </div>
                  </div>
                )) : (
                  <div className="text-center py-4 text-sm text-muted-foreground">
                    No connection history yet
                  </div>
                )}
              </div>
            </ScrollArea>
          </CardContent>
        </Card>

        {/* Direct Connections */}
        {directConnections.length > 0 && (
          <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
            <CardHeader className="pb-3">
              <CardTitle className="text-base font-semibold flex items-center gap-2">
                <Link2 className="h-4 w-4 text-emerald-500" />
                Direct Connections
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="space-y-1.5">
                {directConnections.map((conn) => {
                  const badgeColors: Record<string, string> = {
                    wbstream: 'bg-cyan-500/10 text-cyan-400 border-cyan-500/30',
                    dion: 'bg-violet-500/10 text-violet-400 border-violet-500/30',
                    telemost: 'bg-blue-500/10 text-blue-400 border-blue-500/30',
                    vless: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30',
                    ssh: 'bg-amber-500/10 text-amber-400 border-amber-500/30',
                  }
                  return (
                    <div
                      key={conn.id}
                      className="flex items-center gap-3 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/50 transition-all"
                    >
                      <div className="flex-1 min-w-0">
                        <p className="text-sm font-medium truncate">{conn.label}</p>
                        <p className="text-[11px] font-mono text-muted-foreground truncate">
                          {conn.host}{conn.port ? `:${conn.port}` : ''}
                        </p>
                      </div>
                      <Badge variant="outline" className={`text-[10px] ${badgeColors[conn.protocol] ?? ''}`}>
                        {protocolBadge(conn.protocol)}
                      </Badge>
                    </div>
                  )
                })}
              </div>
            </CardContent>
          </Card>
        )}

        {/* Server Selection */}
        <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
          <CardHeader className="pb-3">
            <CardTitle className="text-base font-semibold flex items-center gap-2">
              <Globe className="h-4 w-4 text-emerald-500" />
              Select Server
            </CardTitle>
          </CardHeader>
          <CardContent>
            <ScrollArea className="max-h-56">
              <div className="space-y-2">
            {onlineServers.map((server: Server, serverIdx: number) => {
              const isLowestPing = server.ping === Math.min(...onlineServers.map((s: Server) => s.ping))
                  const TIcon = transportIcons[server.transportType]
                  return (
                    <motion.button
                      data-testid={`wt-node-${server.id}`}
                      key={server.id}
                      initial={{ opacity: 0, x: -10 }}
                      animate={{ opacity: 1, x: 0 }}
                      transition={{ delay: serverIdx * 0.05 }}
                      className={`flex items-center gap-3 w-full p-3 rounded-lg transition-all text-left ${
                        currentClient?.connectedServerId === server.id
                          ? 'bg-emerald-500/15 border-2 border-emerald-500/50 shadow-sm shadow-emerald-500/10'
                          : 'bg-muted/50 hover:bg-muted border-2 border-transparent hover:border-emerald-500/30 hover:shadow-md hover:shadow-emerald-500/5'
                      }`}
                      onClick={() => handleServerSwitch(server.id)}
                      whileTap={{ scale: 0.97 }}
                    >
                      <span className="text-2xl">{countryFlags[server.countryCode] || '🌐'}</span>
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2">
                          <p className="text-sm font-medium truncate">{server.name}</p>
                          {currentClient?.connectedServerId === server.id && (
                            <Badge className="bg-emerald-500/20 text-emerald-400 border-0 text-[10px] h-4 px-1.5">Connected</Badge>
                          )}
                          {isLowestPing && !currentClient?.connectedServerId && (
                            <Badge className="bg-amber-500/20 text-amber-400 border-0 text-[10px] h-4 px-1.5 flex items-center gap-0.5">
                              <Star className="h-2.5 w-2.5" />
                              Recommended
                            </Badge>
                          )}
                        </div>
                        <div className="flex items-center gap-2 mt-0.5">
                          <SignalBars ping={server.ping} />
                          <span className="text-xs text-muted-foreground">{server.ping}ms</span>
                          <span className="text-xs text-muted-foreground">·</span>
                          <span className="text-xs text-muted-foreground">{Math.round(server.currentLoad)}% load</span>
                        </div>
                        <div className="mt-1.5 flex items-center gap-2">
                          <Progress value={server.currentLoad} className="h-1 flex-1" />
                          <span className="text-[10px] text-muted-foreground">{Math.round(server.currentLoad)}%</span>
                        </div>
                      </div>
                      <div className="flex flex-col items-center gap-1">
                        {TIcon && (
                          <Badge variant="outline" className="text-[9px] px-1.5 h-4 border-cyan-500/30 text-cyan-400 bg-cyan-500/10">
                            {transportLabels[server.transportType] || 'Auto'}
                          </Badge>
                        )}
                        {TIcon && <TIcon className="h-3.5 w-3.5 text-cyan-400 shrink-0" />}
                      </div>
                    </motion.button>
                  )
                })}
                {onlineServers.length === 0 && (
                  <div className="text-center py-6 text-sm text-muted-foreground">
                    No servers available
                  </div>
                )}
              </div>
            </ScrollArea>
          </CardContent>
        </Card>

        {/* Advanced Settings - Better toggle with card border, icon, Expand/Collapse text */}
        <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
          <CardContent className="p-0">
            <button
              className="flex items-center justify-between w-full p-3.5 border border-border rounded-lg m-0 hover:bg-muted/50 transition-all"
              onClick={() => setShowAdvanced(!showAdvanced)}
            >
              <span className="flex items-center gap-2 text-sm font-medium">
                <div className="p-1.5 rounded-md bg-emerald-500/10">
                  <Settings2 className="h-3.5 w-3.5 text-emerald-400" />
                </div>
                Advanced Settings
              </span>
              <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                {showAdvanced ? 'Collapse' : 'Expand'}
                {showAdvanced ? (
                  <ChevronUp className="h-3.5 w-3.5" />
                ) : (
                  <ChevronDown className="h-3.5 w-3.5" />
                )}
              </span>
            </button>

            <AnimatePresence>
              {showAdvanced && currentClient && (
                <motion.div
                  initial={{ height: 0, opacity: 0 }}
                  animate={{ height: 'auto', opacity: 1 }}
                  exit={{ height: 0, opacity: 0 }}
                  transition={{ duration: 0.2 }}
                  className="overflow-hidden"
                >
                  <div className="px-4 pb-4 space-y-5">
                    <Separator />

                    {/* Kill Switch */}
                    <div className="space-y-3">
                      <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                        <Shield className="h-3.5 w-3.5" />
                        Security
                      </div>
                      <div className="space-y-3">
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted/30">
                          <div className="space-y-0.5">
                            <Label className="text-sm font-medium">Kill Switch</Label>
                            <p className="text-[11px] text-muted-foreground">Block all traffic if VPN disconnects unexpectedly</p>
                          </div>
                          <div className={`relative ${killSwitch ? 'shadow-[0_0_12px_rgba(16,185,129,0.4)]' : ''} rounded-full transition-shadow duration-300`}>
                            <Switch
                              checked={killSwitch}
                              onCheckedChange={(checked) => {
                                setKillSwitch(checked)
                                handleUpdateSetting('killSwitch', checked)
                                toast.success(checked ? 'Kill Switch Enabled' : 'Kill Switch Disabled')
                              }}
                            />
                          </div>
                        </div>

                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted/30">
                          <div className="space-y-0.5">
                            <Label className="text-sm font-medium">DNS Leak Protection</Label>
                            <p className="text-[11px] text-muted-foreground">Prevent DNS requests from leaking outside VPN</p>
                          </div>
                          <div className={`relative ${dnsLeakProtection ? 'shadow-[0_0_12px_rgba(16,185,129,0.4)]' : ''} rounded-full transition-shadow duration-300`}>
                            <Switch
                              checked={dnsLeakProtection}
                              onCheckedChange={(checked) => {
                                setDnsLeakProtection(checked)
                                handleUpdateSetting('dnsLeakProtection', checked)
                                toast.success(checked ? 'DNS Leak Protection Enabled' : 'DNS Leak Protection Disabled')
                              }}
                            />
                          </div>
                        </div>
                      </div>
                    </div>

                    {/* Split Tunneling */}
                    <div className="space-y-3">
                      <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                        <ArrowRightLeft className="h-3.5 w-3.5" />
                        Split Tunneling
                      </div>
                      <div className="space-y-3">
                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted/30">
                          <div className="space-y-0.5">
                            <Label className="text-sm font-medium">Split Tunneling</Label>
                            <p className="text-[11px] text-muted-foreground">Choose which apps use VPN</p>
                          </div>
                          <div className={`relative ${splitTunneling ? 'shadow-[0_0_12px_rgba(16,185,129,0.4)]' : ''} rounded-full transition-shadow duration-300`}>
                            <Switch
                              data-testid="wt-split-mode"
                              checked={splitTunneling}
                              onCheckedChange={(checked) => {
                                setSplitTunneling(checked)
                                handleUpdateSetting('splitTunneling', checked)
                              }}
                            />
                          </div>
                        </div>

                        <AnimatePresence>
                          {splitTunneling && (
                            <motion.div
                              initial={{ height: 0, opacity: 0 }}
                              animate={{ height: 'auto', opacity: 1 }}
                              exit={{ height: 0, opacity: 0 }}
                              transition={{ duration: 0.2 }}
                              className="overflow-hidden"
                            >
                              <ScrollArea className="max-h-40">
                                <div className="space-y-1.5 pr-2">
                                  {[
                                    { key: 'browser', name: 'Browser', desc: 'Chrome / Firefox', icon: Globe, defaultOn: true },
                                    { key: 'messaging', name: 'Messaging', desc: 'Telegram / WhatsApp', icon: MessageCircle, defaultOn: true },
                                    { key: 'streaming', name: 'Streaming', desc: 'YouTube / Netflix', icon: Tv, defaultOn: false },
                                    { key: 'gaming', name: 'Gaming', desc: 'Online games', icon: Gamepad2, defaultOn: false },
                                    { key: 'other', name: 'Other', desc: 'All other traffic', icon: AppWindow, defaultOn: true },
                                  ].map((app) => {
                                    const AppIcon = app.icon
                                    const isEnabled = splitTunnelApps[app.key] ?? app.defaultOn
                                    return (
                                      <div
                                        key={app.key}
                                        className="flex items-center justify-between py-2 px-3 rounded-lg bg-muted/30 hover:bg-muted/50 transition-all hover:border-l-2 hover:border-l-emerald-500/30"
                                      >
                                        <div className="flex items-center gap-2.5">
                                          <div className={`p-1.5 rounded-md ${isEnabled ? 'bg-emerald-500/10' : 'bg-muted/50'}`}>
                                            <AppIcon className={`h-3.5 w-3.5 ${isEnabled ? 'text-emerald-400' : 'text-muted-foreground'}`} />
                                          </div>
                                          <div>
                                            <p className={`text-sm ${isEnabled ? 'text-foreground' : 'text-muted-foreground'}`}>{app.name}</p>
                                            <p className="text-[10px] text-muted-foreground">{app.desc}</p>
                                          </div>
                                        </div>
                                        <Switch
                                          checked={isEnabled}
                                          onCheckedChange={(checked) => {
                                            setSplitTunnelApps(prev => ({ ...prev, [app.key]: checked }))
                                          }}
                                          className="scale-90"
                                        />
                                      </div>
                                    )
                                  })}
                                </div>
                              </ScrollArea>
                            </motion.div>
                          )}
                        </AnimatePresence>
                      </div>
                    </div>

                    {/* DNS Settings */}
                    <div className="space-y-3">
                      <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                        <Globe2 className="h-3.5 w-3.5" />
                        DNS Settings
                      </div>
                      <div className="space-y-3">
                        <div className="space-y-2">
                          <Label className="text-sm">DNS Provider</Label>
                          <Select
                            value={dnsProvider}
                            onValueChange={(v) => {
                              setDnsProvider(v)
                              handleUpdateSetting('dnsProvider', v)
                            }}
                          >
                            <SelectTrigger>
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="ultravpn">UltraVPN DNS (Recommended)</SelectItem>
                              <SelectItem value="cloudflare">Cloudflare (1.1.1.1)</SelectItem>
                              <SelectItem value="google">Google (8.8.8.8)</SelectItem>
                              <SelectItem value="custom">Custom</SelectItem>
                            </SelectContent>
                          </Select>
                        </div>

                        <AnimatePresence>
                          {dnsProvider === 'custom' && (
                            <motion.div
                              initial={{ height: 0, opacity: 0 }}
                              animate={{ height: 'auto', opacity: 1 }}
                              exit={{ height: 0, opacity: 0 }}
                              transition={{ duration: 0.2 }}
                              className="overflow-hidden"
                            >
                              <div className="space-y-2">
                                <Label className="text-sm">Custom DNS Address</Label>
                                <Input
                                  placeholder="e.g. 9.9.9.9"
                                  value={customDns}
                                  onChange={(e) => {
                                    setCustomDns(e.target.value)
                                    handleUpdateSetting('customDns', e.target.value)
                                  }}
                                  className="font-mono"
                                />
                              </div>
                            </motion.div>
                          )}
                        </AnimatePresence>
                      </div>
                    </div>

                    {/* Transport Section */}
                    <div className="space-y-3">
                      <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                        <Layers className="h-3.5 w-3.5" />
                        Transport
                      </div>
                      <div className="space-y-2">
                        <Label className="text-sm">Transport Mode</Label>
                        <div className="grid grid-cols-3 gap-2">
                          {(['auto', 'ytp', 'whitelist-bypass'] as TransportType[]).map((mode) => {
                            const Icon = transportIcons[mode]
                            const isActive = currentClient.transportMode === mode
                            return (
                              <button
                                key={mode}
                                className={`flex flex-col items-center gap-1 p-3 rounded-lg transition-all ${
                                  isActive
                                    ? 'bg-emerald-500/15 border border-emerald-500/40'
                                    : 'bg-muted/50 hover:bg-muted border border-transparent'
                                }`}
                                onClick={() => handleUpdateSetting('transportMode', mode)}
                              >
                                <Icon className={`h-4 w-4 ${isActive ? 'text-emerald-400' : 'text-muted-foreground'}`} />
                                <span className={`text-xs ${isActive ? 'text-emerald-400 font-medium' : 'text-muted-foreground'}`}>
                                  {mode === 'ytp' ? 'YTP' : mode === 'whitelist-bypass' ? 'WB' : 'Auto'}
                                </span>
                              </button>
                            )
                          })}
                        </div>
                      </div>
                    </div>

                    {/* Tunnel Section */}
                    <div className="space-y-3">
                      <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                        <Cpu className="h-3.5 w-3.5" />
                        Tunnel
                      </div>
                      <div className="space-y-2">
                        <Label className="text-sm">Tunnel Mode</Label>
                        <div className="grid grid-cols-2 gap-2">
                          {(['dc', 'video'] as TunnelMode[]).map((mode) => {
                            const isActive = currentClient.tunnelMode === mode
                            return (
                              <button
                                key={mode}
                                className={`flex items-center justify-center gap-2 p-3 rounded-lg transition-all ${
                                  isActive
                                    ? 'bg-emerald-500/15 border border-emerald-500/40'
                                    : 'bg-muted/50 hover:bg-muted border border-transparent'
                                }`}
                                onClick={() => handleUpdateSetting('tunnelMode', mode)}
                              >
                                <span className={`text-sm ${isActive ? 'text-emerald-400 font-medium' : 'text-muted-foreground'}`}>
                                  {mode === 'dc' ? 'DC Mode' : 'Video Mode'}
                                </span>
                              </button>
                            )
                          })}
                        </div>
                      </div>
                    </div>

                    {/* Connection Section */}
                    <div className="space-y-3">
                      <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                        <Globe className="h-3.5 w-3.5" />
                        Connection
                      </div>
                      <div className="space-y-3">
                        <div className="space-y-2">
                          <Label className="text-sm">Platform</Label>
                          <Select
                            value={currentClient.platform}
                            onValueChange={(v: Platform) => handleUpdateSetting('platform', v)}
                          >
                            <SelectTrigger>
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value="auto">Auto</SelectItem>
                              <SelectItem value="vk">VK</SelectItem>
                              <SelectItem value="telemost">Telemost</SelectItem>
                              <SelectItem value="wbstream">WB Stream</SelectItem>
                            </SelectContent>
                          </Select>
                        </div>

                        <div className="space-y-2">
                          <Label className="text-sm">SOCKS Port</Label>
                          <Input
                            type="number"
                            value={currentClient.socksPort}
                            onChange={(e) => handleUpdateSetting('socksPort', parseInt(e.target.value) || 1080)}
                            className="font-mono"
                          />
                        </div>

                        <div className="flex items-center justify-between p-3 rounded-lg bg-muted/30">
                          <div className="space-y-0.5">
                            <Label className="text-sm font-medium">Auto-Connect on Startup</Label>
                            <p className="text-[11px] text-muted-foreground">Automatically connect when the app starts</p>
                          </div>
                          <Switch
                            checked={currentClient.autoConnect}
                            onCheckedChange={(checked) => handleUpdateSetting('autoConnect', checked)}
                          />
                        </div>
                      </div>
                    </div>
                  </div>
                </motion.div>
              )}
            </AnimatePresence>
          </CardContent>
        </Card>
      </motion.div>
    </TooltipProvider>
  )

  // Direct connection dialog (shared between mobile and desktop views)
  const directDialog = (
    <DirectConnectionDialog
      open={directDialogOpen}
      onOpenChange={setDirectDialogOpen}
    />
  )

  if (!isDesktopHost) {
    return <>{primaryView}{directDialog}</>
  }

  return (
    <div className="space-y-4">
      <StatusDashboard
        client={currentClient}
        carrierHealth={carrierHealth}
        desktopStatus={desktopStatus}
        smokeTest={smokeTest}
        onRestart={restartRuntime}
      />
      <Tabs defaultValue="connection" className="space-y-4">
        <TabsList className="grid w-full grid-cols-5">
          <TabsTrigger data-testid="wt-tab-connection" value="connection">Connection</TabsTrigger>
          <TabsTrigger data-testid="wt-tab-discovery" value="discovery">Discovery</TabsTrigger>
          <TabsTrigger data-testid="wt-tab-status" value="status">Status</TabsTrigger>
          <TabsTrigger data-testid="wt-tab-logs" value="logs">Logs</TabsTrigger>
          <TabsTrigger data-testid="wt-tab-diagnostics" value="diagnostics">Diagnostics</TabsTrigger>
        </TabsList>
        <TabsContent value="connection">{primaryView}</TabsContent>
        <TabsContent value="discovery">
          <DiscoveryPanel
            nodes={runtimeNodes}
            servers={servers}
            carrierHealth={carrierHealth}
            onRefresh={async () => {
              await fetchServers()
              await refreshDesktopTelemetry()
            }}
            onConnect={async (nodeId) => {
              if (!currentClient) return
              await connectClient(currentClient.id, nodeId)
            }}
          />
        </TabsContent>
        <TabsContent value="status">
          <StatusDashboard
            client={currentClient}
            carrierHealth={carrierHealth}
            desktopStatus={desktopStatus}
            smokeTest={smokeTest}
            onRestart={restartRuntime}
          />
        </TabsContent>
        <TabsContent value="logs">
          <LogViewer logs={daemonLogs.length > 0 ? daemonLogs : logs.map((entry: ConnectionLog) => `${entry.timestamp} ${entry.message}`)} />
        </TabsContent>
        <TabsContent value="diagnostics">
          <SmokeTestPanel
            result={smokeTest}
            onRun={() => runDesktopSmokeTest(currentClient?.connectedServerId)}
          />
        </TabsContent>
      </Tabs>
      {directDialog}
    </div>
  )
}
