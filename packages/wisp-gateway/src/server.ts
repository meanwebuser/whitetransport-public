import Fastify from "fastify";
import { loadConfig } from "./config.js";
import { createTransport } from "./transports/factory.js";
import { registerHealthRoutes } from "./routes/health.js";

const config = loadConfig();
const transport = createTransport(config);

const app = Fastify({
  logger: {
    level: process.env.LOG_LEVEL || "info"
  }
});

await registerHealthRoutes(app, config, transport);

app.server.on("upgrade", (req, socket, head) => {
  const requestUrl = new URL(req.url || "/", `http://${req.headers.host || "localhost"}`);
  if (!requestUrl.pathname.startsWith(config.wispPath)) {
    socket.write("HTTP/1.1 404 Not Found\r\nConnection: close\r\n\r\n");
    socket.destroy();
    return;
  }

  app.log.info({ path: requestUrl.pathname, transport: transport.name }, "routing websocket upgrade");
  transport.routeUpgrade(req, socket, head);
});

const close = async (signal: string) => {
  app.log.info({ signal }, "shutting down");
  await app.close();
  process.exit(0);
};

process.on("SIGINT", () => void close("SIGINT"));
process.on("SIGTERM", () => void close("SIGTERM"));

await app.listen({ host: config.host, port: config.port });
