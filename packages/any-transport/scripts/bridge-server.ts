/**
 * YTP Bridge Server — WebSocket bridge between VK Browser Bridge page
 * and the Y Transport Node.
 *
 * The companion web page (vk-browser-bridge.html) connects to this
 * WebSocket server. Messages flow bidirectionally:
 *
 *   Browser ←→ ws://localhost:9123/bridge ←→ Y Transport Node
 *
 * Usage:
 *   npx ts-node scripts/bridge-server.ts
 *   PORT=9123 npx ts-node scripts/bridge-server.ts
 */

import { createServer, IncomingMessage, ServerResponse } from 'http';
import { WebSocketServer, WebSocket } from 'ws';

const PORT = parseInt(process.env.BRIDGE_PORT || process.env.PORT || '9123', 10);

// ── Simple HTTP server for health check ──────────────────────────────────

const server = createServer((req: IncomingMessage, res: ServerResponse) => {
  res.setHeader('Access-Control-Allow-Origin', '*');
  res.setHeader('Content-Type', 'application/json');

  if (req.url === '/health') {
    res.writeHead(200);
    res.end(JSON.stringify({
      status: 'ok',
      connections: wss.clients.size,
      uptime: process.uptime(),
    }));
    return;
  }

  if (req.url === '/') {
    res.writeHead(200);
    res.end(JSON.stringify({
      name: 'Y Transport Bridge Server',
      version: '0.1.0',
      wsUrl: `ws://localhost:${PORT}/bridge`,
      connections: wss.clients.size,
    }));
    return;
  }

  res.writeHead(404);
  res.end(JSON.stringify({ error: 'Not found' }));
});

// ── WebSocket server ─────────────────────────────────────────────────────

const wss = new WebSocketServer({ server, path: '/bridge' });

interface BridgeClient {
  ws: WebSocket;
  providerType: string;
  connectedAt: number;
  messagesForwarded: number;
}

const clients: Map<WebSocket, BridgeClient> = new Map();

wss.on('connection', (ws: WebSocket, req: IncomingMessage) => {
  const clientInfo: BridgeClient = {
    ws,
    providerType: 'unknown',
    connectedAt: Date.now(),
    messagesForwarded: 0,
  };
  clients.set(ws, clientInfo);

  const ip = req.headers['x-forwarded-for'] || req.socket.remoteAddress;
  console.log(`[Bridge] Client connected from ${ip} (total: ${clients.size})`);

  ws.on('message', (data: Buffer) => {
    try {
      const msg = JSON.parse(data.toString());
      handleClientMessage(ws, msg);
    } catch (err) {
      console.error(`[Bridge] Parse error:`, err);
    }
  });

  ws.on('close', () => {
    clients.delete(ws);
    console.log(`[Bridge] Client disconnected (total: ${clients.size})`);
  });

  ws.on('error', (err) => {
    console.error(`[Bridge] Client error:`, err.message);
    clients.delete(ws);
  });

  // Send welcome
  ws.send(JSON.stringify({
    type: 'connected',
    data: { serverVersion: '0.1.0', bridgePort: PORT },
  }));
});

function handleClientMessage(ws: WebSocket, msg: any): void {
  const client = clients.get(ws);
  if (!client) return;

  switch (msg.type) {
    case 'connected':
      client.providerType = msg.data?.provider || 'unknown';
      console.log(`[Bridge] Client identified as: ${client.providerType}`);
      // Broadcast to other clients (for multi-provider setups)
      broadcastToOthers(ws, {
        type: 'peer-connected',
        data: { provider: client.providerType },
      });
      break;

    case 'messages':
      // Browser received VK messages — forward to other clients
      client.messagesForwarded += (msg.data?.messages?.length || 0);
      console.log(`[Bridge] Forwarding ${msg.data?.messages?.length || 0} messages from ${client.providerType}`);
      broadcastToOthers(ws, msg);
      break;

    case 'append':
      // Someone wants to send a message — forward to browser clients
      console.log(`[Bridge] Append request: ${msg.data?.msgId}`);
      broadcastToOthers(ws, msg);
      break;

    case 'ack':
      // Message sent acknowledgment
      console.log(`[Bridge] ACK: ${msg.data?.msgId} = ${msg.data?.messageId}`);
      broadcastToOthers(ws, msg);
      break;

    case 'scan':
      // Scan request — forward to browser clients
      broadcastToOthers(ws, msg);
      break;

    case 'error':
      console.error(`[Bridge] Client error: ${msg.data?.error}`);
      broadcastToOthers(ws, msg);
      break;

    default:
      console.log(`[Bridge] Unknown message type: ${msg.type}`);
  }
}

function broadcastToOthers(sender: WebSocket, msg: any): void {
  const data = JSON.stringify(msg);
  for (const [ws, client] of clients) {
    if (ws !== sender && ws.readyState === WebSocket.OPEN) {
      ws.send(data);
    }
  }
}

// ── Periodic stats ───────────────────────────────────────────────────────

setInterval(() => {
  if (clients.size > 0) {
    const stats = Array.from(clients.values()).map(c => ({
      provider: c.providerType,
      messages: c.messagesForwarded,
      uptime: Math.floor((Date.now() - c.connectedAt) / 1000),
    }));
    console.log(`[Bridge] Stats: ${clients.size} clients`, JSON.stringify(stats));
  }
}, 30000);

// ── Start ────────────────────────────────────────────────────────────────

server.listen(PORT, () => {
  console.log(`╔══════════════════════════════════════════════════════╗`);
  console.log(`║  Y Transport Bridge Server                           ║`);
  console.log(`║  HTTP:  http://localhost:${PORT}                       ║`);
  console.log(`║  WS:    ws://localhost:${PORT}/bridge                  ║`);
  console.log(`║  Health: http://localhost:${PORT}/health                ║`);
  console.log(`╚══════════════════════════════════════════════════════╝`);
  console.log(`\nWaiting for browser bridge connections...`);
  console.log(`Open docs/vk-browser-bridge.html in your browser\n`);
});
