'use client';

import { useMemo, useState } from 'react';
import { FlaskConical, Loader2 } from 'lucide-react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';

import type { SmokeTestResult } from '../../native/wt-transport';
import { toHumanErrorMessage } from '../../lib/error-messages';

interface SmokeTestPanelProps {
  readonly result: SmokeTestResult | null;
  readonly onRun: () => Promise<SmokeTestResult>;
}

export function SmokeTestPanel(props: SmokeTestPanelProps) {
  const { result, onRun } = props;
  const [running, setRunning] = useState(false);
  const summary = useMemo(() => toHumanErrorMessage(result?.summary), [result]);
  const stepMessage = (detail?: string, error?: string): string => toHumanErrorMessage(error ?? detail ?? 'No detail reported');

  return (
    <Card className="border-border bg-card/80">
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <CardTitle className="flex items-center gap-2 text-base">
          <FlaskConical className="h-4 w-4 text-amber-500" />
          Smoke Test
        </CardTitle>
        <Button
          data-testid="wt-diagnostics-run"
          variant="outline"
          disabled={running}
          onClick={async () => {
            setRunning(true);
            try {
              await onRun();
            } finally {
              setRunning(false);
            }
          }}
        >
          {running ? <Loader2 className="mr-2 h-4 w-4 animate-spin" /> : null}
          Run
        </Button>
      </CardHeader>
      <CardContent className="space-y-3">
        <p className="text-sm text-muted-foreground">
          Verifies daemon health, node discovery, session connect, SOCKS reachability, and disconnect cleanup.
        </p>
        {result && (
          <div className="space-y-3" data-testid="wt-diagnostics-result">
            <div className="flex flex-wrap items-center gap-2">
              <Badge className={result.passed ? 'bg-emerald-500/10 text-emerald-400' : 'bg-red-500/10 text-red-400'}>
                {result.passed ? 'pass' : 'fail'}
              </Badge>
              <Badge variant="outline">{result.totalDurationMs}ms</Badge>
              {result.selectedNodeId ? <Badge variant="outline">{result.selectedNodeId}</Badge> : null}
            </div>
            <p className="text-sm">{summary}</p>
            {(result.directIp || result.externalIp || result.socksIp || result.tunnelIp || result.latencyMs) && (
              <div className="grid gap-2 rounded-lg border border-border bg-muted/20 p-3 text-xs sm:grid-cols-3">
                <div>
                  <p className="text-muted-foreground">External IP</p>
                  <p className="font-mono">{result.directIp ?? result.externalIp ?? 'Not reported'}</p>
                </div>
                <div>
                  <p className="text-muted-foreground">Tunnel IP</p>
                  <p className="font-mono">{result.socksIp ?? result.tunnelIp ?? 'Not reported'}</p>
                </div>
                <div>
                  <p className="text-muted-foreground">Latency</p>
                  <p>{typeof result.latencyMs === 'number' ? `${Math.round(result.latencyMs)}ms` : 'Not reported'}</p>
                </div>
              </div>
            )}
            <div className="space-y-2">
              {result.steps.map((step) => (
                <div key={step.name} className="rounded-lg border border-border bg-muted/20 p-3">
                  <div className="mb-1 flex items-center justify-between gap-3">
                    <span className="text-sm font-medium">{step.name}</span>
                    <div className="flex items-center gap-2">
                      <span className="text-xs text-muted-foreground">{step.durationMs}ms</span>
                      <Badge variant="outline">{step.status}</Badge>
                    </div>
                  </div>
                  <p className={step.error ? 'text-xs text-red-400' : 'text-xs text-muted-foreground'}>{stepMessage(step.detail, step.error)}</p>
                </div>
              ))}
            </div>
          </div>
        )}
      </CardContent>
    </Card>
  );
}
