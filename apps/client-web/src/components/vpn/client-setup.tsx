'use client'

import { useState } from 'react'
import { motion } from 'framer-motion'
import {
  BookOpen,
  CheckCircle2,
  Copy,
  Smartphone,
  Apple,
  Monitor,
  Laptop,
  Terminal,
} from 'lucide-react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Separator } from '@/components/ui/separator'
import { toast } from 'sonner'

interface ClientSetupProps {
  clients: Array<{
    id: string
    name: string
    deviceType: string
    socksPort: number
    transportMode: string
    platform: string
    status: string
  }>
}

const platformTabs = [
  { id: 'android', label: 'Android', icon: Smartphone },
  { id: 'ios', label: 'iOS', icon: Apple },
  { id: 'windows', label: 'Windows', icon: Monitor },
  { id: 'macos', label: 'macOS', icon: Laptop },
  { id: 'linux', label: 'Linux', icon: Terminal },
] as const

interface Step {
  text: string
}

const platformSteps: Record<string, Step[]> = {
  android: [
    { text: 'Download v2rayNG from Google Play or GitHub releases' },
    { text: 'Open the app and tap the "+" button to add a config' },
    { text: 'Select "SOCKS" as the protocol type' },
    { text: 'Enter the server address and port from your assigned server' },
    { text: 'Enable the VPN connection by tapping the connect button' },
    { text: 'Verify connection by checking the UltraVPN dashboard' },
  ],
  ios: [
    { text: 'Download Streisand or Shadowrocket from the App Store' },
    { text: 'Open the app and add a new configuration profile' },
    { text: 'Select SOCKS5 proxy type for the connection' },
    { text: 'Enter the server details provided by your admin' },
    { text: 'Connect and verify in the UltraVPN dashboard' },
  ],
  windows: [
    { text: 'Download v2rayN from the official GitHub releases page' },
    { text: 'Extract and run v2rayN.exe on your computer' },
    { text: 'Click "Server" and then "Add SOCKS server"' },
    { text: 'Enter the server IP and SOCKS5 port provided by your admin' },
    { text: 'Right-click the system tray icon and enable system proxy' },
    { text: 'Verify connection by checking the UltraVPN dashboard' },
  ],
  macos: [
    { text: 'Download ClashX or v2rayU from GitHub releases' },
    { text: 'Install and open the application' },
    { text: 'Add a new SOCKS5 proxy configuration' },
    { text: 'Enter the server address and port from your admin' },
    { text: 'Enable "Set as system proxy" in the menu bar' },
    { text: 'Verify connection by checking the UltraVPN dashboard' },
  ],
  linux: [
    { text: 'Install v2ray or shadowsocks-libev via your package manager' },
    { text: 'Create a config file at ~/.config/v2ray/config.json' },
    { text: 'Configure the SOCKS5 inbound with the server details' },
    { text: 'Start the service with systemctl or the v2ray command' },
    { text: 'Set your system or browser proxy to 127.0.0.1:<local-port>' },
    { text: 'Verify connection by checking the UltraVPN dashboard' },
  ],
}

const platformApps: Record<string, { name: string; url: string }> = {
  android: { name: 'v2rayNG', url: 'https://github.com/2dust/v2rayNG' },
  ios: { name: 'Streisand / Shadowrocket', url: 'https://apps.apple.com' },
  windows: { name: 'v2rayN', url: 'https://github.com/2dust/v2rayN' },
  macos: { name: 'ClashX / v2rayU', url: 'https://github.com/yichengchen/clashX' },
  linux: { name: 'v2ray-core', url: 'https://github.com/v2fly/v2ray-core' },
}

function generateConfig(socksPort: number, transportMode: string): string {
  return `SOCKS5 Proxy:\n  Host: <server-ip>\n  Port: ${socksPort}\n  Transport: ${transportMode}`
}

export function ClientSetup({ clients }: ClientSetupProps) {
  const [activeTab, setActiveTab] = useState('android')

  // Use the first client's config as the default, or fallback values
  const defaultClient = clients[0]
  const socksPort = defaultClient?.socksPort || 1080
  const transportMode = defaultClient?.transportMode || 'auto'

  const handleCopyConfig = async (port: number, transport: string) => {
    const config = generateConfig(port, transport)
    try {
      await navigator.clipboard.writeText(config)
      toast.success('Config copied', { description: 'SOCKS5 proxy config copied to clipboard' })
    } catch {
      toast.error('Failed to copy', { description: 'Could not copy to clipboard' })
    }
  }

  return (
    <motion.div
      initial={{ opacity: 0, y: 20 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.3 }}
    >
      <Card className="bg-card border-border">
        <CardHeader className="pb-3">
          <CardTitle className="text-base font-semibold flex items-center gap-2">
            <BookOpen className="h-5 w-5 text-emerald-500" />
            Client Setup Guide
          </CardTitle>
          <p className="text-sm text-muted-foreground">
            Instructions for connecting devices to UltraVPN
          </p>
        </CardHeader>
        <CardContent>
          <Tabs value={activeTab} onValueChange={setActiveTab}>
            <TabsList className="w-full flex-wrap h-auto gap-1 bg-muted/50 p-1">
              {platformTabs.map((tab) => {
                const Icon = tab.icon
                return (
                  <TabsTrigger
                    key={tab.id}
                    value={tab.id}
                    className="data-[state=active]:bg-emerald-500/20 data-[state=active]:text-emerald-400 data-[state=active]:border-emerald-500/40 flex-1 min-w-0 text-xs gap-1"
                  >
                    <Icon className="h-3.5 w-3.5 shrink-0" />
                    <span className="hidden sm:inline">{tab.label}</span>
                  </TabsTrigger>
                )
              })}
            </TabsList>

            {platformTabs.map((tab) => {
              const steps = platformSteps[tab.id]
              const app = platformApps[tab.id]
              return (
                <TabsContent key={tab.id} value={tab.id} className="mt-4">
                  <ScrollArea className="max-h-96 pr-2">
                    <div className="space-y-4">
                      {/* App Recommendation */}
                      <div className="flex items-center gap-3 p-3 rounded-lg bg-emerald-500/10 border border-emerald-500/20">
                        <Badge variant="outline" className="border-emerald-500/30 text-emerald-400 shrink-0">
                          Recommended
                        </Badge>
                        <div className="min-w-0">
                          <p className="text-sm font-medium text-emerald-300">{app.name}</p>
                          <p className="text-xs text-muted-foreground truncate">{app.url}</p>
                        </div>
                      </div>

                      {/* Steps */}
                      <div className="space-y-3">
                        {steps.map((step, index) => (
                          <motion.div
                            key={index}
                            initial={{ opacity: 0, x: -10 }}
                            animate={{ opacity: 1, x: 0 }}
                            transition={{ delay: index * 0.05 }}
                            className="flex items-start gap-3"
                          >
                            <div className="flex items-center justify-center w-6 h-6 rounded-full bg-emerald-500/20 border border-emerald-500/30 shrink-0 mt-0.5">
                              {index < steps.length - 1 ? (
                                <span className="text-xs font-bold text-emerald-400">{index + 1}</span>
                              ) : (
                                <CheckCircle2 className="h-3.5 w-3.5 text-emerald-400" />
                              )}
                            </div>
                            <p className="text-sm text-muted-foreground leading-relaxed">
                              {step.text}
                            </p>
                          </motion.div>
                        ))}
                      </div>

                      <Separator />

                      {/* Configuration Box */}
                      <div className="space-y-2">
                        <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Configuration
                        </p>
                        <div className="bg-zinc-900 border border-zinc-800 font-mono text-sm p-3 rounded-lg relative group">
                          <pre className="text-emerald-300 whitespace-pre-wrap text-xs sm:text-sm">
                            {generateConfig(socksPort, transportMode)}
                          </pre>
                        </div>
                        <Button
                          variant="outline"
                          size="sm"
                          className="w-full border-emerald-500/30 text-emerald-400 hover:bg-emerald-500/10 hover:text-emerald-300"
                          onClick={() => handleCopyConfig(socksPort, transportMode)}
                        >
                          <Copy className="h-3.5 w-3.5 mr-2" />
                          Copy Config
                        </Button>
                      </div>

                      {/* Per-client configs if multiple clients */}
                      {clients.length > 1 && (
                        <div className="space-y-2">
                          <p className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                            Per-Device Configs
                          </p>
                          <div className="space-y-2">
                            {clients.map((client) => (
                              <div
                                key={client.id}
                                className="flex items-center justify-between p-2 rounded-lg bg-muted/50"
                              >
                                <div className="flex items-center gap-2 min-w-0">
                                  <Badge
                                    variant="outline"
                                    className={`text-xs shrink-0 ${
                                      client.status === 'online'
                                        ? 'border-emerald-500/30 text-emerald-400'
                                        : 'border-zinc-600 text-zinc-400'
                                    }`}
                                  >
                                    {client.name}
                                  </Badge>
                                  <span className="text-xs text-muted-foreground font-mono truncate">
                                    Port {client.socksPort}
                                  </span>
                                </div>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className="h-7 text-xs text-emerald-400 hover:text-emerald-300 shrink-0"
                                  onClick={() => handleCopyConfig(client.socksPort, client.transportMode)}
                                >
                                  <Copy className="h-3 w-3 mr-1" />
                                  Copy
                                </Button>
                              </div>
                            ))}
                          </div>
                        </div>
                      )}
                    </div>
                  </ScrollArea>
                </TabsContent>
              )
            })}
          </Tabs>
        </CardContent>
      </Card>
    </motion.div>
  )
}
