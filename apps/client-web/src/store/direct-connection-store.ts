import { create } from 'zustand';

import {
  parseDirectUri,
  type DirectProtocol,
  type ParsedDirectConnection,
} from '../lib/parse-direct-uri';

// ── Types ────────────────────────────────────────────────────────────

export interface DirectConnection {
  readonly id: string;
  readonly protocol: DirectProtocol;
  readonly label: string;
  readonly rawUri: string;
  readonly host: string;
  readonly port?: number;
  readonly user?: string;
  readonly params: Record<string, string>;
  readonly createdAt: string;
  readonly updatedAt: string;
}

interface DirectConnectionStoreState {
  connections: DirectConnection[];
  /** Parse a URI and add it to the saved list. Returns the new entry or null. */
  addFromUri: (uri: string) => DirectConnection | null;
  /** Add a pre-parsed connection. */
  addConnection: (parsed: ParsedDirectConnection) => DirectConnection;
  /** Update label or raw URI. */
  updateConnection: (id: string, patch: Partial<Pick<DirectConnection, 'label' | 'rawUri'>>) => void;
  /** Remove a saved connection. */
  removeConnection: (id: string) => void;
  /** Reorder (move item from oldIndex to newIndex). */
  reorder: (oldIndex: number, newIndex: number) => void;
}

// ── Persistence ──────────────────────────────────────────────────────

const STORAGE_KEY = 'wt_direct_connections';

function loadFromStorage(): DirectConnection[] {
  try {
    if (typeof localStorage === 'undefined') return [];
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed: DirectConnection[] = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function saveToStorage(items: DirectConnection[]): void {
  try {
    if (typeof localStorage === 'undefined') return;
    localStorage.setItem(STORAGE_KEY, JSON.stringify(items));
  } catch {
    // ignore quota errors
  }
}

// ── Helpers ──────────────────────────────────────────────────────────

function genId(): string {
  return `dc-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

const now = (): string => new Date().toISOString();

// ── Store ────────────────────────────────────────────────────────────

export const useDirectConnectionStore = create<DirectConnectionStoreState>((set, get) => ({
  connections: loadFromStorage(),

  addFromUri(uri: string): DirectConnection | null {
    const parsed = parseDirectUri(uri);
    if (!parsed) return null;
    return get().addConnection(parsed);
  },

  addConnection(parsed: ParsedDirectConnection): DirectConnection {
    const ts = now();
    const entry: DirectConnection = {
      id: genId(),
      protocol: parsed.protocol,
      label: parsed.label,
      rawUri: parsed.rawUri,
      host: parsed.host,
      port: parsed.port,
      user: parsed.user,
      params: parsed.params,
      createdAt: ts,
      updatedAt: ts,
    };
    const next = [entry, ...get().connections];
    set({ connections: next });
    saveToStorage(next);
    return entry;
  },

  updateConnection(id: string, patch: Partial<Pick<DirectConnection, 'label' | 'rawUri'>>): void {
    const next = get().connections.map((c) => {
      if (c.id !== id) return c;
      const updated = { ...c, ...patch, updatedAt: now() };
      // Re-parse if rawUri changed
      if (patch.rawUri && patch.rawUri !== c.rawUri) {
        const reParsed = parseDirectUri(patch.rawUri);
        if (reParsed) {
          return {
            ...updated,
            protocol: reParsed.protocol,
            host: reParsed.host,
            port: reParsed.port,
            user: reParsed.user,
            params: reParsed.params,
          } as DirectConnection;
        }
      }
      return updated;
    });
    set({ connections: next });
    saveToStorage(next);
  },

  removeConnection(id: string): void {
    const next = get().connections.filter((c) => c.id !== id);
    set({ connections: next });
    saveToStorage(next);
  },

  reorder(oldIndex: number, newIndex: number): void {
    const items = [...get().connections];
    const [moved] = items.splice(oldIndex, 1);
    if (moved) items.splice(newIndex, 0, moved);
    set({ connections: items });
    saveToStorage(items);
  },
}));
