'use client';

import { useCallback, useMemo, useState } from 'react';
import {
  Globe,
  Link2,
  Monitor,
  Pencil,
  Plus,
  Radio,
  Shield,
  Trash2,
  Video,
  X,
} from 'lucide-react';

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { ScrollArea } from '@/components/ui/scroll-area';

import {
  detectProtocol,
  parseDirectUri,
  protocolBadge,
  type DirectProtocol,
} from '../../lib/parse-direct-uri';
import {
  useDirectConnectionStore,
  type DirectConnection,
} from '../../store/direct-connection-store';

// ── Protocol styling ────────────────────────────────────────────────

const protocolStyles: Record<DirectProtocol, { color: string; icon: React.ElementType }> = {
  wbstream: { color: 'text-cyan-400 bg-cyan-500/10 border-cyan-500/30', icon: Video },
  dion: { color: 'text-violet-400 bg-violet-500/10 border-violet-500/30', icon: Radio },
  telemost: { color: 'text-blue-400 bg-blue-500/10 border-blue-500/30', icon: Monitor },
  vless: { color: 'text-emerald-400 bg-emerald-500/10 border-emerald-500/30', icon: Shield },
  ssh: { color: 'text-amber-400 bg-amber-500/10 border-amber-500/30', icon: Monitor },
};

const EXAMPLE_URIS = [
  'wbstream://room-abc123',
  'dion://call-id?token=abc',
  'telemost://my-room',
  'vless://uuid@host:443?type=tcp#MyServer',
  'ssh://user:pass@192.168.1.1:22',
];

// ── Component ────────────────────────────────────────────────────────

interface DirectConnectionDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Called when user clicks "Connect" on a saved entry. */
  onConnect?: (connection: DirectConnection) => void;
}

export function DirectConnectionDialog({
  open,
  onOpenChange,
  onConnect,
}: DirectConnectionDialogProps) {
  const connections = useDirectConnectionStore((s) => s.connections);
  const addFromUri = useDirectConnectionStore((s) => s.addFromUri);
  const removeConnection = useDirectConnectionStore((s) => s.removeConnection);
  const updateConnection = useDirectConnectionStore((s) => s.updateConnection);

  const [uriInput, setUriInput] = useState('');
  const [error, setError] = useState<string | null>(null);

  // editing state
  const [editingId, setEditingId] = useState<string | null>(null);
  const [editLabel, setEditLabel] = useState('');
  const [editUri, setEditUri] = useState('');

  // delete confirmation
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null);

  const detectedProtocol = useMemo(() => detectProtocol(uriInput), [uriInput]);

  const handleAdd = useCallback(() => {
    setError(null);
    const result = addFromUri(uriInput);
    if (!result) {
      setError('Не удалось распознать URI. Проверьте формат.');
      return;
    }
    setUriInput('');
  }, [addFromUri, uriInput]);

  const handleFillExample = useCallback((example: string) => {
    setUriInput(example);
    setError(null);
  }, []);

  const startEdit = useCallback((conn: DirectConnection) => {
    setEditingId(conn.id);
    setEditLabel(conn.label);
    setEditUri(conn.rawUri);
  }, []);

  const saveEdit = useCallback(() => {
    if (!editingId) return;
    updateConnection(editingId, { label: editLabel, rawUri: editUri });
    setEditingId(null);
  }, [editingId, editLabel, editUri, updateConnection]);

  const confirmDelete = useCallback(() => {
    if (!deleteTarget) return;
    removeConnection(deleteTarget);
    setDeleteTarget(null);
  }, [deleteTarget, removeConnection]);

  return (
    <>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Link2 className="h-5 w-5 text-emerald-400" />
              Прямые подключения
            </DialogTitle>
            <DialogDescription>
              Добавьте URI для прямого подключения к серверам, минуя discovery.
            </DialogDescription>
          </DialogHeader>

          {/* URI input */}
          <div className="space-y-2">
            <div className="flex gap-2">
              <Input
                placeholder="wbstream://, dion://, telemost://, vless://, ssh://..."
                value={uriInput}
                onChange={(e) => {
                  setUriInput(e.target.value);
                  setError(null);
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') handleAdd();
                }}
                className="flex-1 font-mono text-sm"
              />
              <Button onClick={handleAdd} disabled={!uriInput.trim()} size="sm">
                <Plus className="mr-1 h-4 w-4" />
                Добавить
              </Button>
            </div>

            {/* Detected protocol badge */}
            {detectedProtocol && (
              <Badge variant="outline" className={`${protocolStyles[detectedProtocol].color} text-xs`}>
                {protocolBadge(detectedProtocol)}
              </Badge>
            )}

            {error && <p className="text-xs text-red-400">{error}</p>}

            {/* Example chips */}
            <div className="flex flex-wrap gap-1.5 pt-1">
              {EXAMPLE_URIS.map((ex) => (
                <button
                  key={ex}
                  type="button"
                  onClick={() => handleFillExample(ex)}
                  className="rounded-full border border-border bg-muted/30 px-2 py-0.5 text-[11px] font-mono text-muted-foreground hover:bg-muted/60 hover:text-foreground transition-colors truncate max-w-[200px]"
                >
                  {ex}
                </button>
              ))}
            </div>
          </div>

          {/* Saved connections list */}
          {connections.length > 0 && (
            <div className="space-y-1.5">
              <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Сохранённые ({connections.length})
              </p>
              <ScrollArea className="max-h-[280px]">
                <div className="space-y-1.5 pr-2">
                  {connections.map((conn) => {
                    const style = protocolStyles[conn.protocol];
                    const Icon = style.icon;
                    const isEditing = editingId === conn.id;

                    return (
                      <div
                        key={conn.id}
                        className="flex items-center gap-2 rounded-lg border border-border bg-muted/20 p-2.5 hover:bg-muted/40 transition-colors"
                      >
                        {isEditing ? (
                          /* Inline edit mode */
                          <div className="flex-1 space-y-1.5">
                            <Input
                              value={editLabel}
                              onChange={(e) => setEditLabel(e.target.value)}
                              placeholder="Название"
                              className="h-7 text-sm"
                            />
                            <Input
                              value={editUri}
                              onChange={(e) => setEditUri(e.target.value)}
                              placeholder="URI"
                              className="h-7 font-mono text-xs"
                            />
                            <div className="flex gap-1.5">
                              <Button size="sm" variant="default" className="h-6 text-xs" onClick={saveEdit}>
                                Сохранить
                              </Button>
                              <Button
                                size="sm"
                                variant="ghost"
                                className="h-6 text-xs"
                                onClick={() => setEditingId(null)}
                              >
                                Отмена
                              </Button>
                            </div>
                          </div>
                        ) : (
                          /* Display mode */
                          <>
                            <div className={`shrink-0 rounded-md p-1.5 ${style.color.split(' ').slice(1).join(' ')}`}>
                              <Icon className="h-3.5 w-3.5" />
                            </div>
                            <button
                              type="button"
                              className="flex-1 min-w-0 text-left"
                              onClick={() => onConnect?.(conn)}
                            >
                              <p className="text-sm font-medium truncate">{conn.label}</p>
                              <p className="text-[11px] font-mono text-muted-foreground truncate">
                                {conn.host}{conn.port ? `:${conn.port}` : ''}
                              </p>
                            </button>
                            <Badge variant="outline" className={`shrink-0 text-[10px] ${style.color}`}>
                              {protocolBadge(conn.protocol)}
                            </Badge>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7 shrink-0"
                              onClick={() => startEdit(conn)}
                            >
                              <Pencil className="h-3.5 w-3.5 text-muted-foreground" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="h-7 w-7 shrink-0"
                              onClick={() => setDeleteTarget(conn.id)}
                            >
                              <Trash2 className="h-3.5 w-3.5 text-red-400" />
                            </Button>
                          </>
                        )}
                      </div>
                    );
                  })}
                </div>
              </ScrollArea>
            </div>
          )}

          {connections.length === 0 && (
            <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed border-border p-6 text-center">
              <Globe className="h-8 w-8 text-muted-foreground/50" />
              <p className="text-sm text-muted-foreground">
                Нет сохранённых подключений
              </p>
              <p className="text-xs text-muted-foreground">
                Вставьте URI и нажмите «Добавить»
              </p>
            </div>
          )}
        </DialogContent>
      </Dialog>

      {/* Delete confirmation dialog */}
      <AlertDialog open={!!deleteTarget} onOpenChange={(o) => { if (!o) setDeleteTarget(null); }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Удалить подключение?</AlertDialogTitle>
            <AlertDialogDescription>
              Это действие нельзя отменить. Подключение будет удалено из списка.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Отмена</AlertDialogCancel>
            <AlertDialogAction onClick={confirmDelete} className="bg-red-600 hover:bg-red-700">
              Удалить
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}
