'use client';

import { useCallback, useEffect, useMemo, useState } from 'react';
import { Activity, Bug, CheckCircle2, FileText, Globe2, Link2, Plus, Power, RefreshCw, Settings2, Server, TerminalSquare, XCircle } from 'lucide-react';
import { toast } from 'sonner';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip';

import { useClientStore, type ConnectionLog, type Server as ServerModel } from '../../store/client-store';
import { useDirectConnectionStore } from '../../store/direct-connection-store';
import { protocolBadge } from '../../lib/parse-direct-uri';
import { DirectConnectionDialog } from './direct-connection-dialog';

const statusText: Record<string, string> = {
  connected: 'Подключено',
  starting: 'Подключение',
  discovering: 'Поиск серверов',
  degraded: 'Есть проблемы',
  error: 'Ошибка',
  stopped: 'Отключено',
};

function formatLatency(value?: number): string {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? `${Math.round(value)} ms` : '—';
}

function formatServerLatency(server: ServerModel): string {
  return server.ping > 0 ? `${Math.round(server.ping)} ms` : '—';
}

function serverStatus(server: ServerModel): string {
  if (server.status === 'online') return 'online';
  if (server.status === 'degraded') return 'degraded';
  return 'offline';
}

export function DesktopClientView() {
  const {
    servers,
    clients,
    logs,
    desktopStatus,
    daemonLogs,
    carrierHealth,
    smokeTest,
    fetchServers,
    fetchClients,
    fetchLogs,
    connectClient,
    disconnectClient,
    refreshDesktopTelemetry,
    runDesktopSmokeTest,
    restartRuntime,
  } = useClientStore();

  const directConnections = useDirectConnectionStore((s) => s.connections);

  const [selectedServerId, setSelectedServerId] = useState<string>('');
  const [isConnecting, setIsConnecting] = useState(false);
  const [activeTab, setActiveTab] = useState('connection');
  const [isDiagnosticsRunning, setIsDiagnosticsRunning] = useState(false);
  const [directDialogOpen, setDirectDialogOpen] = useState(false);

  const currentClient = clients[0];
  const runtimeStatus = desktopStatus?.status ?? currentClient?.status ?? 'stopped';
  const isOnline = currentClient?.status === 'online' || runtimeStatus === 'connected';
  const availableServers = useMemo(() => servers.filter((server) => server.status !== 'offline'), [servers]);
  const selectedServer = availableServers.find((server) => server.id === selectedServerId)
    ?? availableServers.find((server) => server.id === currentClient?.connectedServerId)
    ?? availableServers[0]
    ?? null;
  const externalIp = desktopStatus?.externalIp ?? desktopStatus?.socksIp ?? desktopStatus?.tunnelIp;
  const visibleLogs = daemonLogs.length > 0
    ? daemonLogs
    : logs.map((entry: ConnectionLog) => `${entry.timestamp} ${entry.message}`);

  useEffect(() => {
    void fetchClients();
    void fetchServers();
    void fetchLogs();
    void refreshDesktopTelemetry();
    const timer = window.setInterval(() => {
      void fetchServers();
      void refreshDesktopTelemetry();
    }, 5000);
    return () => window.clearInterval(timer);
  }, [fetchClients, fetchLogs, fetchServers, refreshDesktopTelemetry]);

  const handleToggle = useCallback(async () => {
    if (!currentClient) return;
    setIsConnecting(true);
    try {
      if (isOnline) {
        await disconnectClient(currentClient.id);
        toast.success('Отключено');
      } else {
        await connectClient(currentClient.id, selectedServer?.id);
        toast.success('Подключено', { description: selectedServer?.name ?? 'Auto' });
      }
      await fetchServers();
      await refreshDesktopTelemetry();
    } catch (error) {
      toast.error('Не удалось подключиться', { description: error instanceof Error ? error.message : String(error) });
    } finally {
      setIsConnecting(false);
    }
  }, [connectClient, currentClient, disconnectClient, fetchServers, isOnline, refreshDesktopTelemetry, selectedServer]);

  const refreshNodes = useCallback(async () => {
    await fetchServers();
    await refreshDesktopTelemetry();
  }, [fetchServers, refreshDesktopTelemetry]);

  const runDiagnostics = useCallback(async () => {
    setIsDiagnosticsRunning(true);
    try {
      await runDesktopSmokeTest(currentClient?.connectedServerId || selectedServer?.id);
      await refreshDesktopTelemetry();
    } finally {
      setIsDiagnosticsRunning(false);
    }
  }, [currentClient?.connectedServerId, refreshDesktopTelemetry, runDesktopSmokeTest, selectedServer?.id]);

  const hasServers = availableServers.length > 0;
  const tabCount = hasServers ? 3 : 4;

  return (
    <TooltipProvider>
      <div className="min-h-screen bg-background text-foreground">
        <div className="mx-auto flex min-h-screen w-full max-w-5xl flex-col px-5 py-5">
          <header className="mb-5 flex items-center justify-between gap-4">
            <div className="flex items-center gap-3">
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="outline"
                    size="icon"
                    className="h-10 w-10 rounded-full border-emerald-500/30 text-emerald-400 hover:bg-emerald-500/10"
                    onClick={() => setDirectDialogOpen(true)}
                  >
                    <Plus className="h-5 w-5" />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Добавить прямое подключение</TooltipContent>
              </Tooltip>
              <div>
                <h1 className="text-xl font-semibold tracking-normal">WhiteTransport</h1>
                <p className="text-sm text-muted-foreground">{desktopStatus?.message ?? 'Готово к подключению'}</p>
              </div>
            </div>
            <Tooltip>
              <TooltipTrigger asChild>
                <Button variant="outline" size="icon" onClick={() => setActiveTab(activeTab === 'connection' ? 'diagnostics' : 'connection')}>
                  <Settings2 className="h-5 w-5" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Настроить и диагностика</TooltipContent>
            </Tooltip>
          </header>

          <main className="flex-1">
            <section className="mx-auto flex max-w-xl flex-col items-center text-center">
              <button
                data-testid="wt-connect-toggle"
                type="button"
                disabled={isConnecting}
                onClick={handleToggle}
                className={`relative flex h-36 w-36 items-center justify-center rounded-full border transition ${
                  isOnline
                    ? 'border-emerald-400/60 bg-emerald-500/20 shadow-[0_0_45px_rgba(16,185,129,0.25)]'
                    : 'border-border bg-card hover:border-emerald-500/50 hover:bg-emerald-500/10'
                }`}
              >
                {isConnecting && <span className="absolute inset-2 animate-pulse rounded-full border border-cyan-400/50" />}
                <Power className={`h-12 w-12 ${isOnline ? 'text-emerald-300' : 'text-muted-foreground'}`} />
              </button>
              <p data-testid="wt-status" className={`mt-4 text-lg font-semibold ${isOnline ? 'text-emerald-300' : 'text-muted-foreground'}`}>
                {isConnecting ? 'Подключение...' : statusText[runtimeStatus] ?? runtimeStatus}
              </p>
              <div className="mt-3 grid w-full grid-cols-2 gap-3 text-left">
                <div className="rounded-lg border border-border bg-card p-3">
                  <p className="text-xs text-muted-foreground">Внешний IP</p>
                  <p data-testid="wt-external-ip" className="mt-1 truncate font-mono text-sm">{externalIp ?? '—'}</p>
                </div>
                <div className="rounded-lg border border-border bg-card p-3">
                  <p className="text-xs text-muted-foreground">Задержка</p>
                  <p className="mt-1 font-mono text-sm">{formatLatency(desktopStatus?.latencyMs)}</p>
                </div>
              </div>
            </section>

            <Tabs value={activeTab} onValueChange={setActiveTab} className="mt-6 space-y-4">
              <TabsList className={`grid w-full grid-cols-${tabCount}`}>
                <TabsTrigger data-testid="wt-tab-connection" value="connection">Серверы</TabsTrigger>
                {!hasServers && <TabsTrigger data-testid="wt-tab-discovery" value="discovery">Discovery</TabsTrigger>}
                <TabsTrigger data-testid="wt-tab-logs" value="logs">Логи</TabsTrigger>
                <TabsTrigger data-testid="wt-tab-diagnostics" value="diagnostics">Debug</TabsTrigger>
              </TabsList>

              <TabsContent value="connection" className="space-y-3">
                {/* Direct connections */}
                {directConnections.length > 0 && (
                  <div className="space-y-1.5">
                    <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground px-1">
                      Прямые подключения
                    </p>
                    {directConnections.map((conn) => {
                      const styleMap: Record<string, string> = {
                        wbstream: 'border-cyan-500/30 bg-cyan-500/5 text-cyan-400',
                        dion: 'border-violet-500/30 bg-violet-500/5 text-violet-400',
                        telemost: 'border-blue-500/30 bg-blue-500/5 text-blue-400',
                        vless: 'border-emerald-500/30 bg-emerald-500/5 text-emerald-400',
                        ssh: 'border-amber-500/30 bg-amber-500/5 text-amber-400',
                      };
                      const badgeMap: Record<string, string> = {
                        wbstream: 'bg-cyan-500/10 text-cyan-400 border-cyan-500/30',
                        dion: 'bg-violet-500/10 text-violet-400 border-violet-500/30',
                        telemost: 'bg-blue-500/10 text-blue-400 border-blue-500/30',
                        vless: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30',
                        ssh: 'bg-amber-500/10 text-amber-400 border-amber-500/30',
                      };
                      return (
                        <button
                          key={conn.id}
                          type="button"
                          className={`grid w-full grid-cols-[1fr_auto] items-center gap-3 rounded-lg border p-3 text-left transition hover:border-emerald-500/30 ${styleMap[conn.protocol] ?? 'border-border bg-card'}`}
                        >
                          <span className="min-w-0">
                            <span className="flex items-center gap-2">
                              <Link2 className="h-4 w-4" />
                              <span className="truncate font-medium">{conn.label}</span>
                              <Badge variant="outline" className={`text-[10px] ${badgeMap[conn.protocol] ?? ''}`}>
                                {protocolBadge(conn.protocol)}
                              </Badge>
                            </span>
                            <span className="mt-1 block truncate font-mono text-xs text-muted-foreground">
                              {conn.host}{conn.port ? `:${conn.port}` : ''}
                            </span>
                          </span>
                        </button>
                      );
                    })}
                  </div>
                )}
                <ServerList
                  servers={availableServers}
                  selectedServerId={selectedServer?.id}
                  onSelect={setSelectedServerId}
                  onConnect={async (serverId) => {
                    setSelectedServerId(serverId);
                    if (!currentClient) return;
                    setIsConnecting(true);
                    try {
                      await connectClient(currentClient.id, serverId);
                      await refreshDesktopTelemetry();
                    } finally {
                      setIsConnecting(false);
                    }
                  }}
                />
              </TabsContent>

              <TabsContent value="discovery" className="space-y-4">
                <div className="flex items-center justify-between gap-3">
                  <div>
                    <h2 className="text-base font-semibold">Найденные серверы</h2>
                    <p className="text-sm text-muted-foreground">Список берется из runtime discovery.</p>
                  </div>
                  <Button data-testid="wt-refresh-nodes" variant="outline" onClick={refreshNodes}>
                    <RefreshCw className="mr-2 h-4 w-4" />
                    Обновить
                  </Button>
                </div>
                <ServerList servers={availableServers} selectedServerId={selectedServer?.id} onSelect={setSelectedServerId} onConnect={async (serverId) => connectClient(currentClient?.id ?? 'this-device', serverId)} />
                <CarrierSummary carrierHealth={carrierHealth} />
              </TabsContent>

              <TabsContent value="logs">
                <Card className="border-border bg-card/80">
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2 text-base">
                      <FileText className="h-4 w-4 text-cyan-400" />
                      Логи
                    </CardTitle>
                    <p className="break-all text-xs text-muted-foreground">{desktopStatus?.logFilePath ?? 'Файл логов еще не открыт'}</p>
                  </CardHeader>
                  <CardContent>
                    <ScrollArea className="h-[420px] rounded-md border border-border bg-black/30 p-3">
                      <div data-testid="wt-logs" className="space-y-2 font-mono text-xs text-slate-200">
                        {visibleLogs.length > 0 ? visibleLogs.map((line, index) => (
                          <div key={`${index}-${line.slice(0, 32)}`} className="break-all">{line}</div>
                        )) : <p className="text-muted-foreground">Логов пока нет.</p>}
                      </div>
                    </ScrollArea>
                  </CardContent>
                </Card>
              </TabsContent>

              <TabsContent value="diagnostics" className="space-y-4">
                <Card className="border-border bg-card/80">
                  <CardHeader>
                    <CardTitle className="flex items-center gap-2 text-base">
                      <Bug className="h-4 w-4 text-amber-400" />
                      Runtime debug
                    </CardTitle>
                  </CardHeader>
                  <CardContent className="space-y-3 text-sm">
                    <DebugRow label="daemon" value={`${desktopStatus?.daemonState ?? 'unknown'} ${desktopStatus?.pid ? `(pid ${desktopStatus.pid})` : ''}`} />
                    <DebugRow label="binary" value={desktopStatus?.runtimePath ?? '—'} />
                    <DebugRow label="config" value={desktopStatus?.configPath ?? '—'} />
                    <DebugRow label="token-store" value={desktopStatus?.tokenStorePath ?? '—'} />
                    <DebugRow label="logs" value={desktopStatus?.logFilePath ?? '—'} />
                    <div className="flex flex-wrap gap-2 pt-2">
                      <Button data-testid="wt-diagnostics-run" onClick={runDiagnostics} disabled={isDiagnosticsRunning}>
                        <Activity className="mr-2 h-4 w-4" />
                        {isDiagnosticsRunning ? 'Проверка...' : 'Run diagnostics'}
                      </Button>
                      <Button variant="outline" onClick={restartRuntime}>Restart daemon</Button>
                    </div>
                  </CardContent>
                </Card>

                <Card className="border-border bg-card/80">
                  <CardHeader>
                    <CardTitle className="text-base">Diagnostics result</CardTitle>
                  </CardHeader>
                  <CardContent data-testid="wt-diagnostics-result" className="space-y-3 text-sm">
                    {smokeTest ? (
                      <>
                        <div className="flex items-center gap-2">
                          {smokeTest.passed ? <CheckCircle2 className="h-4 w-4 text-emerald-400" /> : <XCircle className="h-4 w-4 text-red-400" />}
                          <span>{smokeTest.summary}</span>
                        </div>
                        <div className="grid gap-2 md:grid-cols-3">
                          <DebugRow label="Direct IP" value={smokeTest.directIp ?? '—'} />
                          <DebugRow label="SOCKS IP" value={smokeTest.socksIp ?? '—'} />
                          <DebugRow label="Latency" value={formatLatency(smokeTest.latencyMs)} />
                        </div>
                      </>
                    ) : (
                      <p className="text-muted-foreground">Диагностика еще не запускалась.</p>
                    )}
                  </CardContent>
                </Card>
              </TabsContent>
            </Tabs>
          </main>
        </div>
      </div>
      <DirectConnectionDialog
        open={directDialogOpen}
        onOpenChange={setDirectDialogOpen}
      />
    </TooltipProvider>
  );
}

function ServerList(props: {
  readonly servers: ServerModel[];
  readonly selectedServerId?: string;
  readonly onSelect: (serverId: string) => void;
  readonly onConnect: (serverId: string) => Promise<void>;
}) {
  const { servers, selectedServerId, onSelect, onConnect } = props;
  return (
    <div data-testid="wt-node-list" className="space-y-2">
      {servers.map((server) => (
        <button
          data-testid={`wt-node-${server.id}`}
          key={server.id}
          type="button"
          onClick={() => onSelect(server.id)}
          onDoubleClick={() => void onConnect(server.id)}
          className={`grid w-full grid-cols-[1fr_auto] items-center gap-3 rounded-lg border p-3 text-left transition ${
            selectedServerId === server.id ? 'border-emerald-500/60 bg-emerald-500/10' : 'border-border bg-card hover:border-emerald-500/30'
          }`}
        >
          <span className="min-w-0">
            <span className="flex items-center gap-2">
              <Server className="h-4 w-4 text-emerald-400" />
              <span className="truncate font-medium">{server.name}</span>
              <Badge variant="outline" className="text-[11px]">{serverStatus(server)}</Badge>
            </span>
            <span className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
              <Globe2 className="h-3.5 w-3.5" />
              {server.city || server.country}
            </span>
          </span>
          <span className="font-mono text-sm text-muted-foreground">{formatServerLatency(server)}</span>
        </button>
      ))}
      {servers.length === 0 && (
        <div className="rounded-lg border border-dashed border-border p-6 text-center text-sm text-muted-foreground">
          Серверы пока не найдены. Нажмите обновить или включите подключение в auto mode.
        </div>
      )}
    </div>
  );
}

function CarrierSummary(props: { readonly carrierHealth: Record<string, { readonly healthy?: boolean; readonly last_error?: string }> }) {
  const entries = Object.entries(props.carrierHealth);
  return (
    <Card className="border-border bg-card/80">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <TerminalSquare className="h-4 w-4 text-cyan-400" />
          Carrier health
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-2 text-sm">
        {entries.length > 0 ? entries.map(([id, snapshot]) => (
          <div key={id} className="flex items-center justify-between gap-3 rounded-md border border-border bg-muted/20 p-2">
            <span className="font-mono text-xs">{id}</span>
            <Badge className={snapshot.healthy ? 'bg-emerald-500/10 text-emerald-400' : 'bg-amber-500/10 text-amber-400'}>
              {snapshot.healthy ? 'healthy' : snapshot.last_error ? 'error' : 'unknown'}
            </Badge>
          </div>
        )) : <p className="text-muted-foreground">Carrier health появится после старта daemon.</p>}
      </CardContent>
    </Card>
  );
}

function DebugRow(props: { readonly label: string; readonly value: string }) {
  return (
    <div className="grid gap-1 rounded-md border border-border bg-muted/20 p-2 md:grid-cols-[120px_1fr]">
      <span className="text-xs uppercase text-muted-foreground">{props.label}</span>
      <span className="break-all font-mono text-xs">{props.value}</span>
    </div>
  );
}
