'use client'

import { motion, AnimatePresence } from 'framer-motion'
import {
  Power, Shield, ShieldCheck, Globe, ChevronDown, ChevronUp,
  ArrowDown, ArrowUp, Signal, HardDrive, Radio, Layers, Cpu,
  ArrowRightLeft, Star, Network, Lock, RefreshCw, Settings2, Gamepad2,
  MessageCircle, Tv, AppWindow, Globe2, Link2,
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
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { toast } from 'sonner'
import { type Server, type Client, type TransportType, type TunnelMode, type Platform } from '../../store/client-store'
import { protocolBadge, type DirectProtocol } from '../../lib/parse-direct-uri'
import {
  countryFlags, transportIcons, transportLabels, vpnProtocolNames,
  formatBytes,
} from './client-view-utils'

// ─── Data usage ring chart ───────────────────────────────────────────────────
export function DataUsageRing({ used, max }: { used: number; max: number }) {
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
        <circle cx="60" cy="60" r={radius} stroke="hsl(var(--muted))" strokeWidth="10" fill="none" />
        <motion.circle
          cx="60" cy="60" r={radius} stroke="url(#ringGradient)" strokeWidth="10" fill="none"
          strokeLinecap="round" strokeDasharray={circumference}
          initial={{ strokeDashoffset: circumference }} animate={{ strokeDashoffset: offset }}
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

// ─── Signal strength bars ────────────────────────────────────────────────────
export function SignalBars({ ping }: { ping: number }) {
  const activeBars = ping < 20 ? 5 : ping < 50 ? 4 : ping < 100 ? 3 : ping < 200 ? 2 : 1
  return (
    <div className="flex items-end gap-0.5 h-4">
      {Array.from({ length: 5 }).map((_, i) => (
        <div key={i} className={`w-1.5 rounded-sm transition-all ${
          i < activeBars ? activeBars >= 4 ? 'bg-emerald-500' : activeBars >= 3 ? 'bg-yellow-500' : 'bg-red-500' : 'bg-muted-foreground/20'
        }`} style={{ height: `${(i + 1) * 20}%` }} />
      ))}
    </div>
  )
}

// ─── Segmented quality gauge ─────────────────────────────────────────────────
export function SegmentedGauge({ quality }: { quality: string }) {
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
            <div key={i} className={`flex-1 rounded-sm transition-all duration-500 ${
              i < config.filled ? config.color : 'bg-muted-foreground/15'
            }`} />
          ))}
        </div>
        <div className="absolute inset-0 flex items-center justify-center">
          <span className="text-xs font-bold text-white drop-shadow-md">{config.label}</span>
        </div>
      </div>
    </div>
  )
}

// ─── Server info card ────────────────────────────────────────────────────────
export function ServerInfoCard({
  connectedServer, TransportIcon, currentClient, isConnecting, onReconnect,
}: {
  connectedServer: Server; TransportIcon: React.ElementType | null
  currentClient: Client | undefined; isConnecting: boolean; onReconnect: () => void
}) {
  return (
    <motion.div initial={{ opacity: 0, y: 10 }} animate={{ opacity: 1, y: 0 }}>
      <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-emerald-500 relative overflow-hidden">
        <div className="absolute inset-x-0 top-0 h-1 overflow-hidden">
          {[...Array(5)].map((_, i) => (
            <motion.div key={i} className="absolute h-1 w-3 rounded-full bg-emerald-400/60" style={{ top: 0 }}
              animate={{ left: ['-10%', '110%'], opacity: [0, 1, 1, 0] }}
              transition={{ repeat: Infinity, duration: 2.5, delay: i * 0.5, ease: 'linear' }} />
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
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button variant="ghost" size="icon" className="h-7 w-7 rounded-full hover:bg-emerald-500/10"
                    onClick={onReconnect} disabled={isConnecting}>
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
  )
}

// ─── Security info panel ─────────────────────────────────────────────────────
export function SecurityInfoPanel({
  dnsLeakProtection, killSwitch, splitTunneling, connectedServer,
}: {
  dnsLeakProtection: boolean; killSwitch: boolean; splitTunneling: boolean; connectedServer: Server
}) {
  return (
    <motion.div initial={{ opacity: 0, y: 10 }} animate={{ opacity: 1, y: 0 }} transition={{ delay: 0.1 }}>
      <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border overflow-hidden relative">
        <div className="absolute inset-0 pointer-events-none" style={{ background: 'linear-gradient(135deg, rgba(16,185,129,0.04) 0%, rgba(6,182,212,0.03) 50%, transparent 100%)' }} />
        <CardHeader className="pb-3 pt-4 px-4 relative">
          <CardTitle className="text-sm font-semibold flex items-center gap-2">
            <ShieldCheck className="h-4 w-4 text-emerald-500" /> Security Info
          </CardTitle>
        </CardHeader>
        <CardContent className="px-4 pb-4 relative">
          <div className="grid grid-cols-2 gap-2.5">
            <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
              <div className="p-1.5 rounded-full bg-emerald-500/10 shrink-0"><Lock className="h-3.5 w-3.5 text-emerald-400" /></div>
              <div className="min-w-0">
                <p className="text-[10px] text-muted-foreground">Encryption</p>
                <Badge className="bg-emerald-500/15 text-emerald-400 border-emerald-500/30 text-[9px] h-4 px-1.5 mt-0.5">AES-256-GCM</Badge>
              </div>
            </div>
            <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
              <div className={`p-1.5 rounded-full shrink-0 ${dnsLeakProtection ? 'bg-emerald-500/10' : 'bg-red-500/10'}`}>
                <ShieldCheck className={`h-3.5 w-3.5 ${dnsLeakProtection ? 'text-emerald-400' : 'text-red-400'}`} />
              </div>
              <div className="min-w-0">
                <p className="text-[10px] text-muted-foreground">DNS Leak</p>
                <Badge className={`text-[9px] h-4 px-1.5 mt-0.5 ${dnsLeakProtection ? 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30' : 'bg-red-500/15 text-red-400 border-red-500/30'}`}>
                  {dnsLeakProtection ? 'Protected' : 'Exposed'}
                </Badge>
              </div>
            </div>
            <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
              <div className={`p-1.5 rounded-full shrink-0 ${killSwitch ? 'bg-emerald-500/10' : 'bg-red-500/10'}`}>
                <Power className={`h-3.5 w-3.5 ${killSwitch ? 'text-emerald-400' : 'text-red-400'}`} />
              </div>
              <div className="min-w-0">
                <p className="text-[10px] text-muted-foreground">Kill Switch</p>
                <Badge className={`text-[9px] h-4 px-1.5 mt-0.5 ${killSwitch ? 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30' : 'bg-red-500/15 text-red-400 border-red-500/30'}`}>
                  {killSwitch ? 'Active' : 'Inactive'}
                </Badge>
              </div>
            </div>
            <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
              <div className="p-1.5 rounded-full bg-cyan-500/10 shrink-0"><Radio className="h-3.5 w-3.5 text-cyan-400" /></div>
              <div className="min-w-0">
                <p className="text-[10px] text-muted-foreground">Protocol</p>
                <Badge className="bg-cyan-500/15 text-cyan-400 border-cyan-500/30 text-[9px] h-4 px-1.5 mt-0.5 truncate max-w-full">
                  {vpnProtocolNames[connectedServer.transportType] || 'Auto (YTP+WB)'}
                </Badge>
              </div>
            </div>
            <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
              <div className={`p-1.5 rounded-full shrink-0 ${splitTunneling ? 'bg-amber-500/10' : 'bg-emerald-500/10'}`}>
                <ArrowRightLeft className={`h-3.5 w-3.5 ${splitTunneling ? 'text-amber-400' : 'text-emerald-400'}`} />
              </div>
              <div className="min-w-0">
                <p className="text-[10px] text-muted-foreground">Split Tunnel</p>
                <Badge className={`text-[9px] h-4 px-1.5 mt-0.5 ${splitTunneling ? 'bg-amber-500/15 text-amber-400 border-amber-500/30' : 'bg-emerald-500/15 text-emerald-400 border-emerald-500/30'}`}>
                  {splitTunneling ? 'Custom rules' : 'All traffic'}
                </Badge>
              </div>
            </div>
            <div className="flex items-center gap-2.5 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/40 transition-colors">
              <div className="p-1.5 rounded-full bg-emerald-500/10 shrink-0"><Globe2 className="h-3.5 w-3.5 text-emerald-400" /></div>
              <div className="min-w-0">
                <p className="text-[10px] text-muted-foreground">WebRTC Leak</p>
                <Badge className="bg-emerald-500/15 text-emerald-400 border-emerald-500/30 text-[9px] h-4 px-1.5 mt-0.5">Protected</Badge>
              </div>
            </div>
          </div>
        </CardContent>
      </Card>
    </motion.div>
  )
}

// ─── Stats row ───────────────────────────────────────────────────────────────
export function StatsRow({
  downloadSpeed, uploadSpeed, connectedServer, currentClient,
}: {
  downloadSpeed: number; uploadSpeed: number; connectedServer: Server | null | undefined; currentClient: Client
}) {
  return (
    <motion.div initial={{ opacity: 0, y: 20 }} animate={{ opacity: 1, y: 0 }} className="grid grid-cols-2 gap-3">
      <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-emerald-500 hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300">
        <CardContent className="p-3 flex items-center gap-3">
          <div className="p-2 rounded-full bg-emerald-500/10 shrink-0"><ArrowDown className="h-4 w-4 text-emerald-500" /></div>
          <div className="min-w-0">
            <p className="text-xs uppercase tracking-widest text-muted-foreground">Download</p>
            <p className="text-2xl font-bold">{downloadSpeed} <span className="text-xs font-normal text-muted-foreground">Mbps</span></p>
          </div>
        </CardContent>
      </Card>
      <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-cyan-500 hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300">
        <CardContent className="p-3 flex items-center gap-3">
          <div className="p-2 rounded-full bg-cyan-500/10 shrink-0"><ArrowUp className="h-4 w-4 text-cyan-500" /></div>
          <div className="min-w-0">
            <p className="text-xs uppercase tracking-widest text-muted-foreground">Upload</p>
            <p className="text-2xl font-bold">{uploadSpeed} <span className="text-xs font-normal text-muted-foreground">Mbps</span></p>
          </div>
        </CardContent>
      </Card>
      <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-amber-500 hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300">
        <CardContent className="p-3 flex items-center gap-3">
          <div className="p-2 rounded-full bg-amber-500/10 shrink-0"><Signal className="h-4 w-4 text-amber-500" /></div>
          <div className="min-w-0">
            <p className="text-xs uppercase tracking-widest text-muted-foreground">Ping</p>
            <p className="text-2xl font-bold">{connectedServer?.ping || 0}<span className="text-xs font-normal text-muted-foreground">ms</span></p>
          </div>
        </CardContent>
      </Card>
      <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border border-l-4 border-l-teal-500 hover:shadow-lg hover:-translate-y-0.5 transition-all duration-300">
        <CardContent className="p-3 flex items-center gap-3">
          <div className="p-2 rounded-full bg-teal-500/10 shrink-0"><HardDrive className="h-4 w-4 text-teal-500" /></div>
          <div className="min-w-0">
            <p className="text-xs uppercase tracking-widest text-muted-foreground">Data</p>
            <p className="text-2xl font-bold">{formatBytes(currentClient.totalDataUsed)}</p>
          </div>
        </CardContent>
      </Card>
    </motion.div>
  )
}

// ─── Data usage section ──────────────────────────────────────────────────────
export function DataUsageSection({
  bandwidthUsed, bandwidthLimit, downloadSpeed, uploadSpeed,
}: {
  bandwidthUsed: number; bandwidthLimit: number; downloadSpeed: number; uploadSpeed: number
}) {
  return (
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
  )
}

// ─── Direct connections panel ────────────────────────────────────────────────
export function DirectConnectionsPanel({ connections }: { connections: Array<{ id: string; label: string; host: string; port?: number; protocol: DirectProtocol }> }) {
  const badgeColors: Record<string, string> = {
    wbstream: 'bg-cyan-500/10 text-cyan-400 border-cyan-500/30',
    dion: 'bg-violet-500/10 text-violet-400 border-violet-500/30',
    telemost: 'bg-blue-500/10 text-blue-400 border-blue-500/30',
    vless: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30',
    ssh: 'bg-amber-500/10 text-amber-400 border-amber-500/30',
  }
  return (
    <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
      <CardHeader className="pb-3">
        <CardTitle className="text-base font-semibold flex items-center gap-2">
          <Link2 className="h-4 w-4 text-emerald-500" /> Direct Connections
        </CardTitle>
      </CardHeader>
      <CardContent>
        <div className="space-y-1.5">
          {connections.map((conn) => (
            <div key={conn.id} className="flex items-center gap-3 p-2.5 rounded-lg bg-muted/30 hover:bg-muted/50 transition-all">
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
          ))}
        </div>
      </CardContent>
    </Card>
  )
}

// ─── Server selection panel ──────────────────────────────────────────────────
export function ServerSelectionPanel({
  onlineServers, currentClient, onServerSwitch,
}: {
  onlineServers: Server[]; currentClient: Client | undefined; onServerSwitch: (serverId: string) => void
}) {
  return (
    <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
      <CardHeader className="pb-3">
        <CardTitle className="text-base font-semibold flex items-center gap-2">
          <Globe className="h-4 w-4 text-emerald-500" /> Select Server
        </CardTitle>
      </CardHeader>
      <CardContent>
        <ScrollArea className="max-h-56">
          <div className="space-y-2">
            {onlineServers.map((server: Server, serverIdx: number) => {
              const isLowestPing = server.ping === Math.min(...onlineServers.map((s: Server) => s.ping))
              const TIcon = transportIcons[server.transportType]
              return (
                <motion.button data-testid={`wt-node-${server.id}`} key={server.id}
                  initial={{ opacity: 0, x: -10 }} animate={{ opacity: 1, x: 0 }} transition={{ delay: serverIdx * 0.05 }}
                  className={`flex items-center gap-3 w-full p-3 rounded-lg transition-all text-left ${
                    currentClient?.connectedServerId === server.id
                      ? 'bg-emerald-500/15 border-2 border-emerald-500/50 shadow-sm shadow-emerald-500/10'
                      : 'bg-muted/50 hover:bg-muted border-2 border-transparent hover:border-emerald-500/30 hover:shadow-md hover:shadow-emerald-500/5'
                  }`}
                  onClick={() => onServerSwitch(server.id)} whileTap={{ scale: 0.97 }}>
                  <span className="text-2xl">{countryFlags[server.countryCode] || '🌐'}</span>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-medium truncate">{server.name}</p>
                      {currentClient?.connectedServerId === server.id && (
                        <Badge className="bg-emerald-500/20 text-emerald-400 border-0 text-[10px] h-4 px-1.5">Connected</Badge>
                      )}
                      {isLowestPing && !currentClient?.connectedServerId && (
                        <Badge className="bg-amber-500/20 text-amber-400 border-0 text-[10px] h-4 px-1.5 flex items-center gap-0.5">
                          <Star className="h-2.5 w-2.5" /> Recommended
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
              <div className="text-center py-6 text-sm text-muted-foreground">No servers available</div>
            )}
          </div>
        </ScrollArea>
      </CardContent>
    </Card>
  )
}

// ─── Advanced settings panel ─────────────────────────────────────────────────
export function AdvancedSettingsPanel({
  showAdvanced, onToggleAdvanced, currentClient,
  killSwitch, onKillSwitchChange,
  dnsLeakProtection, onDnsLeakChange,
  splitTunneling, onSplitTunnelChange,
  splitTunnelApps, onSplitTunnelAppChange,
  dnsProvider, onDnsProviderChange,
  customDns, onCustomDnsChange,
  onUpdateSetting,
}: {
  showAdvanced: boolean; onToggleAdvanced: () => void; currentClient: Client
  killSwitch: boolean; onKillSwitchChange: (v: boolean) => void
  dnsLeakProtection: boolean; onDnsLeakChange: (v: boolean) => void
  splitTunneling: boolean; onSplitTunnelChange: (v: boolean) => void
  splitTunnelApps: Record<string, boolean>; onSplitTunnelAppChange: (key: string, v: boolean) => void
  dnsProvider: string; onDnsProviderChange: (v: string) => void
  customDns: string; onCustomDnsChange: (v: string) => void
  onUpdateSetting: (field: string, value: unknown) => void
}) {
  return (
    <Card className="bg-card/80 backdrop-blur-sm ring-1 ring-white/5 border-border">
      <CardContent className="p-0">
        <button className="flex items-center justify-between w-full p-3.5 border border-border rounded-lg m-0 hover:bg-muted/50 transition-all"
          onClick={onToggleAdvanced}>
          <span className="flex items-center gap-2 text-sm font-medium">
            <div className="p-1.5 rounded-md bg-emerald-500/10"><Settings2 className="h-3.5 w-3.5 text-emerald-400" /></div>
            Advanced Settings
          </span>
          <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            {showAdvanced ? 'Collapse' : 'Expand'}
            {showAdvanced ? <ChevronUp className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
          </span>
        </button>
        <AnimatePresence>
          {showAdvanced && (
            <motion.div initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }}
              exit={{ height: 0, opacity: 0 }} transition={{ duration: 0.2 }} className="overflow-hidden">
              <div className="px-4 pb-4 space-y-5">
                <Separator />
                <div className="space-y-3">
                  <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                    <Shield className="h-3.5 w-3.5" /> Security
                  </div>
                  <div className="space-y-3">
                    <div className="flex items-center justify-between p-3 rounded-lg bg-muted/30">
                      <div className="space-y-0.5">
                        <Label className="text-sm font-medium">Kill Switch</Label>
                        <p className="text-[11px] text-muted-foreground">Block all traffic if VPN disconnects unexpectedly</p>
                      </div>
                      <div className={`relative ${killSwitch ? 'shadow-[0_0_12px_rgba(16,185,129,0.4)]' : ''} rounded-full transition-shadow duration-300`}>
                        <Switch checked={killSwitch} onCheckedChange={(checked) => {
                          onKillSwitchChange(checked); onUpdateSetting('killSwitch', checked)
                          toast.success(checked ? 'Kill Switch Enabled' : 'Kill Switch Disabled')
                        }} />
                      </div>
                    </div>
                    <div className="flex items-center justify-between p-3 rounded-lg bg-muted/30">
                      <div className="space-y-0.5">
                        <Label className="text-sm font-medium">DNS Leak Protection</Label>
                        <p className="text-[11px] text-muted-foreground">Prevent DNS requests from leaking outside VPN</p>
                      </div>
                      <div className={`relative ${dnsLeakProtection ? 'shadow-[0_0_12px_rgba(16,185,129,0.4)]' : ''} rounded-full transition-shadow duration-300`}>
                        <Switch checked={dnsLeakProtection} onCheckedChange={(checked) => {
                          onDnsLeakChange(checked); onUpdateSetting('dnsLeakProtection', checked)
                          toast.success(checked ? 'DNS Leak Protection Enabled' : 'DNS Leak Protection Disabled')
                        }} />
                      </div>
                    </div>
                  </div>
                </div>
                <div className="space-y-3">
                  <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                    <ArrowRightLeft className="h-3.5 w-3.5" /> Split Tunneling
                  </div>
                  <div className="space-y-3">
                    <div className="flex items-center justify-between p-3 rounded-lg bg-muted/30">
                      <div className="space-y-0.5">
                        <Label className="text-sm font-medium">Split Tunneling</Label>
                        <p className="text-[11px] text-muted-foreground">Choose which apps use VPN</p>
                      </div>
                      <div className={`relative ${splitTunneling ? 'shadow-[0_0_12px_rgba(16,185,129,0.4)]' : ''} rounded-full transition-shadow duration-300`}>
                        <Switch data-testid="wt-split-mode" checked={splitTunneling}
                          onCheckedChange={(checked) => { onSplitTunnelChange(checked); onUpdateSetting('splitTunneling', checked) }} />
                      </div>
                    </div>
                    <AnimatePresence>
                      {splitTunneling && (
                        <motion.div initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }}
                          exit={{ height: 0, opacity: 0 }} transition={{ duration: 0.2 }} className="overflow-hidden">
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
                                  <div key={app.key} className="flex items-center justify-between py-2 px-3 rounded-lg bg-muted/30 hover:bg-muted/50 transition-all hover:border-l-2 hover:border-l-emerald-500/30">
                                    <div className="flex items-center gap-2.5">
                                      <div className={`p-1.5 rounded-md ${isEnabled ? 'bg-emerald-500/10' : 'bg-muted/50'}`}>
                                        <AppIcon className={`h-3.5 w-3.5 ${isEnabled ? 'text-emerald-400' : 'text-muted-foreground'}`} />
                                      </div>
                                      <div>
                                        <p className={`text-sm ${isEnabled ? 'text-foreground' : 'text-muted-foreground'}`}>{app.name}</p>
                                        <p className="text-[10px] text-muted-foreground">{app.desc}</p>
                                      </div>
                                    </div>
                                    <Switch checked={isEnabled} onCheckedChange={(checked) => onSplitTunnelAppChange(app.key, checked)} className="scale-90" />
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
                <div className="space-y-3">
                  <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                    <Globe2 className="h-3.5 w-3.5" /> DNS Settings
                  </div>
                  <div className="space-y-3">
                    <div className="space-y-2">
                      <Label className="text-sm">DNS Provider</Label>
                      <Select value={dnsProvider} onValueChange={(v) => { onDnsProviderChange(v); onUpdateSetting('dnsProvider', v) }}>
                        <SelectTrigger><SelectValue /></SelectTrigger>
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
                        <motion.div initial={{ height: 0, opacity: 0 }} animate={{ height: 'auto', opacity: 1 }}
                          exit={{ height: 0, opacity: 0 }} transition={{ duration: 0.2 }} className="overflow-hidden">
                          <div className="space-y-2">
                            <Label className="text-sm">Custom DNS Address</Label>
                            <Input placeholder="e.g. 9.9.9.9" value={customDns}
                              onChange={(e) => { onCustomDnsChange(e.target.value); onUpdateSetting('customDns', e.target.value) }}
                              className="font-mono" />
                          </div>
                        </motion.div>
                      )}
                    </AnimatePresence>
                  </div>
                </div>
                <div className="space-y-3">
                  <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                    <Layers className="h-3.5 w-3.5" /> Transport
                  </div>
                  <div className="space-y-2">
                    <Label className="text-sm">Transport Mode</Label>
                    <div className="grid grid-cols-3 gap-2">
                      {(['auto', 'ytp', 'whitelist-bypass'] as TransportType[]).map((mode) => {
                        const Icon = transportIcons[mode]
                        const isActive = currentClient.transportMode === mode
                        return (
                          <button key={mode}
                            className={`flex flex-col items-center gap-1 p-3 rounded-lg transition-all ${
                              isActive ? 'bg-emerald-500/15 border border-emerald-500/40' : 'bg-muted/50 hover:bg-muted border border-transparent'
                            }`}
                            onClick={() => onUpdateSetting('transportMode', mode)}>
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
                <div className="space-y-3">
                  <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                    <Cpu className="h-3.5 w-3.5" /> Tunnel
                  </div>
                  <div className="space-y-2">
                    <Label className="text-sm">Tunnel Mode</Label>
                    <div className="grid grid-cols-2 gap-2">
                      {(['dc', 'video'] as TunnelMode[]).map((mode) => {
                        const isActive = currentClient.tunnelMode === mode
                        return (
                          <button key={mode}
                            className={`flex items-center justify-center gap-2 p-3 rounded-lg transition-all ${
                              isActive ? 'bg-emerald-500/15 border border-emerald-500/40' : 'bg-muted/50 hover:bg-muted border border-transparent'
                            }`}
                            onClick={() => onUpdateSetting('tunnelMode', mode)}>
                            <span className={`text-sm ${isActive ? 'text-emerald-400 font-medium' : 'text-muted-foreground'}`}>
                              {mode === 'dc' ? 'DC Mode' : 'Video Mode'}
                            </span>
                          </button>
                        )
                      })}
                    </div>
                  </div>
                </div>
                <div className="space-y-3">
                  <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                    <Globe className="h-3.5 w-3.5" /> Connection
                  </div>
                  <div className="space-y-3">
                    <div className="space-y-2">
                      <Label className="text-sm">Platform</Label>
                      <Select value={currentClient.platform} onValueChange={(v: Platform) => onUpdateSetting('platform', v)}>
                        <SelectTrigger><SelectValue /></SelectTrigger>
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
                      <Input type="number" value={currentClient.socksPort}
                        onChange={(e) => onUpdateSetting('socksPort', parseInt(e.target.value) || 1080)} className="font-mono" />
                    </div>
                    <div className="flex items-center justify-between p-3 rounded-lg bg-muted/30">
                      <div className="space-y-0.5">
                        <Label className="text-sm font-medium">Auto-Connect on Startup</Label>
                        <p className="text-[11px] text-muted-foreground">Automatically connect when the app starts</p>
                      </div>
                      <Switch checked={currentClient.autoConnect} onCheckedChange={(checked) => onUpdateSetting('autoConnect', checked)} />
                    </div>
                  </div>
                </div>
              </div>
            </motion.div>
          )}
        </AnimatePresence>
      </CardContent>
    </Card>
  )
}
