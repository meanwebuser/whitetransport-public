/**
 * YTP Session Manager — manages the lifecycle of Y Transport sessions.
 */

export interface Session {
  sessionId: string;
  peerNodeId: string;
  myNodeId: string;
  status: 'handshaking' | 'active' | 'paused' | 'closed';
  currentEpoch: number;
  createdAt: number;
  lastActiveAt: number;
}

export class SessionManager {
  private sessions: Map<string, Session> = new Map();

  create(sessionId: string, peerNodeId: string, myNodeId: string): Session {
    const session: Session = {
      sessionId,
      peerNodeId,
      myNodeId,
      status: 'handshaking',
      currentEpoch: 1,
      createdAt: Date.now(),
      lastActiveAt: Date.now(),
    };
    this.sessions.set(sessionId, session);
    return session;
  }

  get(sessionId: string): Session | undefined {
    return this.sessions.get(sessionId);
  }

  activate(sessionId: string): void {
    const session = this.sessions.get(sessionId);
    if (session) {
      session.status = 'active';
      session.lastActiveAt = Date.now();
    }
  }

  pause(sessionId: string): void {
    const session = this.sessions.get(sessionId);
    if (session) session.status = 'paused';
  }

  close(sessionId: string): void {
    const session = this.sessions.get(sessionId);
    if (session) session.status = 'closed';
  }

  advanceEpoch(sessionId: string): void {
    const session = this.sessions.get(sessionId);
    if (session) session.currentEpoch++;
  }

  get active(): Session[] {
    return [...this.sessions.values()].filter(s => s.status === 'active');
  }

  get all(): Session[] {
    return [...this.sessions.values()];
  }
}
