'use client';

import { Radio, RefreshCw, Wifi, WifiOff } from 'lucide-react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { ScrollArea } from '@/components/ui/scroll-area';

import type { CarrierHealthSnapshot, RuntimeNode } from '../../native/wt-transport';
import type { Server } from '../../store/client-store';

interface DiscoveryPanelProps {
  readonly nodes: RuntimeNode[];
  readonly servers: Server[];
  readonly carrierHealth: Record<string, CarrierHealthSnapshot>;
  readonly onRefresh: () => Promise<void>;
  readonly onConnect: (nodeId: string) => Promise<void>;
}

export function DiscoveryPanel(props: DiscoveryPanelProps) {
  const { nodes, servers, carrierHealth, onRefresh, onConnect } = props;
  const availableServers = servers.filter((server) => server.status !== 'offline');

  return (
    <div className="grid gap-4 lg:grid-cols-[1.1fr,0.9fr]">
      <Card className="border-border bg-card/80">
        <CardHeader className="flex flex-row items-center justify-between space-y-0">
          <CardTitle className="flex items-center gap-2 text-base">
            <Radio className="h-4 w-4 text-emerald-500" />
            Discovery
          </CardTitle>
          <Button data-testid="wt-refresh-nodes" variant="outline" size="sm" onClick={onRefresh}>
            <RefreshCw className="mr-2 h-4 w-4" />
            Refresh
          </Button>
        </CardHeader>
        <CardContent>
          <ScrollArea className="h-[320px] pr-3">
            <div className="space-y-3" data-testid="wt-node-list">
              {availableServers.map((server) => {
                const node = nodes.find((candidate) => candidate.node_id === server.id);
                const statusIcon = server.status === 'online' ? Wifi : WifiOff;
                const StatusIcon = statusIcon;
                return (
                  <button
                    data-testid={`wt-node-${server.id}`}
                    key={server.id}
                    className="flex w-full items-start justify-between rounded-lg border border-border bg-muted/30 p-3 text-left transition hover:bg-muted/50"
                    onClick={() => onConnect(server.id)}
                    type="button"
                  >
                    <div className="space-y-1">
                      <div className="flex items-center gap-2">
                        <StatusIcon className={`h-4 w-4 ${server.status === 'online' ? 'text-emerald-500' : 'text-amber-500'}`} />
                        <span className="text-sm font-medium">{server.name}</span>
                        <Badge variant="outline" className="text-[11px]">
                          {server.status}
                        </Badge>
                      </div>
                      <p className="text-xs text-muted-foreground">
                        Node ID: {server.id}
                      </p>
                      {node?.last_seen_at && (
                        <p className="text-xs text-muted-foreground">
                          Last seen: {new Date(node.last_seen_at).toLocaleString()}
                        </p>
                      )}
                    </div>
                    <Badge className="bg-emerald-500/10 text-emerald-400">
                      Connect
                    </Badge>
                  </button>
                );
              })}
              {availableServers.length === 0 && (
                <p className="text-sm text-muted-foreground">No runtime nodes are currently available.</p>
              )}
            </div>
          </ScrollArea>
        </CardContent>
      </Card>

      <Card className="border-border bg-card/80">
        <CardHeader>
          <CardTitle className="text-base">Carrier Health</CardTitle>
        </CardHeader>
        <CardContent>
          <ScrollArea className="h-[320px] pr-3">
            <div className="space-y-3">
              {Object.entries(carrierHealth).map(([carrierId, snapshot]) => (
                <div key={carrierId} className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="mb-2 flex items-center justify-between gap-3">
                    <span className="text-sm font-medium">{carrierId}</span>
                    <Badge className={snapshot.healthy ? 'bg-emerald-500/10 text-emerald-400' : 'bg-amber-500/10 text-amber-400'}>
                      {snapshot.healthy ? 'healthy' : 'degraded'}
                    </Badge>
                  </div>
                  <div className="grid grid-cols-2 gap-2 text-xs text-muted-foreground">
                    <span>Read ok: {snapshot.read_successes ?? 0}</span>
                    <span>Read fail: {snapshot.read_failures ?? 0}</span>
                    <span>Write ok: {snapshot.write_successes ?? 0}</span>
                    <span>Write fail: {snapshot.write_failures ?? 0}</span>
                  </div>
                  {snapshot.last_error && (
                    <p className="mt-2 text-xs text-amber-400">{String(snapshot.last_error)}</p>
                  )}
                </div>
              ))}
              {Object.keys(carrierHealth).length === 0 && (
                <p className="text-sm text-muted-foreground">Carrier health data will appear after the daemon starts.</p>
              )}
            </div>
          </ScrollArea>
        </CardContent>
      </Card>
    </div>
  );
}
