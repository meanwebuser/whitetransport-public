'use client';

import { ScrollArea } from '@/components/ui/scroll-area';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';

interface LogViewerProps {
  readonly logs: readonly string[];
}

export function LogViewer(props: LogViewerProps) {
  const { logs } = props;

  return (
    <Card className="border-border bg-card/80">
      <CardHeader>
        <CardTitle className="text-base">Daemon Logs</CardTitle>
      </CardHeader>
      <CardContent>
        <ScrollArea className="h-[420px] rounded-md border border-border bg-black/30 p-3">
          <div className="space-y-2 font-mono text-xs text-slate-200">
            {logs.map((line, index) => (
              <div key={`${index}-${line.slice(0, 32)}`} className="break-all">
                {line}
              </div>
            ))}
            {logs.length === 0 && (
              <p className="text-muted-foreground">No daemon logs captured yet.</p>
            )}
          </div>
        </ScrollArea>
      </CardContent>
    </Card>
  );
}
