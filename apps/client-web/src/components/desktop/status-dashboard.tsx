'use client';

import { Activity, RotateCcw, Shield, TerminalSquare } from 'lucide-react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';

import type { CarrierHealthSnapshot, DesktopRuntimeStatus, SmokeTestResult } from '../../native/wt-transport';
import type { Client } from '../../store/client-store';

interface StatusDashboardProps {
  readonly client?: Client;
  readonly carrierHealth: Record<string, CarrierHealthSnapshot>;
  readonly desktopStatus?: DesktopRuntimeStatus | null;
  readonly smokeTest?: SmokeTestResult | null;
  readonly onRestart: () => Promise<void>;
}

const firstProvided = (...values: Array<string | undefined>): string | undefined => values.find((value) => value && value.trim() !== '');

const formatLatency = (value?: number): string => (typeof value === 'number' && Number.isFinite(value) ? `${Math.round(value)}ms` : 'Not reported');

export function StatusDashboard(props: StatusDashboardProps) {
  const { client, carrierHealth, desktopStatus, smokeTest, onRestart } = props;
  const healthyCarriers = Object.values(carrierHealth).filter((snapshot) => snapshot.healthy).length;
  const totalCarriers = Object.keys(carrierHealth).length;
  const externalIp = firstProvided(smokeTest?.directIp, smokeTest?.externalIp, desktopStatus?.directIp, desktopStatus?.externalIp);
  const tunnelIp = firstProvided(smokeTest?.socksIp, smokeTest?.tunnelIp, desktopStatus?.socksIp, desktopStatus?.tunnelIp);
  const latencyMs = smokeTest?.latencyMs ?? desktopStatus?.latencyMs;

  return (
    <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
      <Card className="border-border bg-card/80">
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-sm">
            <Shield className="h-4 w-4 text-emerald-500" />
            Session
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-1 text-sm">
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">State</span>
            <Badge className={client?.status === 'online' ? 'bg-emerald-500/10 text-emerald-400' : 'bg-muted text-muted-foreground'}>
              {client?.status ?? 'offline'}
            </Badge>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">Server</span>
            <span>{client?.connectedServer?.name ?? 'None'}</span>
          </div>
          <div className="flex items-center justify-between gap-3">
            <span className="text-muted-foreground">External IP</span>
            <span data-testid="wt-external-ip" className="truncate font-mono text-xs">{externalIp ?? 'Not reported'}</span>
          </div>
        </CardContent>
      </Card>

      <Card className="border-border bg-card/80">
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-sm">
            <TerminalSquare className="h-4 w-4 text-cyan-500" />
            SOCKS
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-1 text-sm">
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">Host</span>
            <span>127.0.0.1</span>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">Port</span>
            <span data-testid="wt-socks-port">{client?.socksPort ?? 0}</span>
          </div>
          <div className="flex items-center justify-between gap-3">
            <span className="text-muted-foreground">Tunnel IP</span>
            <span className="truncate font-mono text-xs">{tunnelIp ?? 'Not reported'}</span>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">Latency</span>
            <span>{formatLatency(latencyMs)}</span>
          </div>
        </CardContent>
      </Card>

      <Card className="border-border bg-card/80">
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-sm">
            <Activity className="h-4 w-4 text-amber-500" />
            Carriers
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-1 text-sm">
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">Healthy</span>
            <span>{healthyCarriers}</span>
          </div>
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">Total</span>
            <span>{totalCarriers}</span>
          </div>
        </CardContent>
      </Card>

      <Card className="border-border bg-card/80">
        <CardHeader className="pb-2">
          <CardTitle className="flex items-center gap-2 text-sm">
            <RotateCcw className="h-4 w-4 text-violet-500" />
            Runtime
          </CardTitle>
        </CardHeader>
        <CardContent>
          <Button className="w-full" variant="outline" onClick={onRestart}>
            Restart Daemon
          </Button>
        </CardContent>
      </Card>
    </div>
  );
}
