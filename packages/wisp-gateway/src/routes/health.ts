import type { FastifyInstance } from "fastify";
import type { AppConfig } from "../config.js";
import type { UpgradeTransport } from "../transports/index.js";

export async function registerHealthRoutes(app: FastifyInstance, config: AppConfig, transport: UpgradeTransport): Promise<void> {
  app.get("/health", async () => ({
    ok: true,
    service: "whitetransport",
    mode: config.mode,
    wispPath: config.wispPath
  }));

  app.get("/debug/config", async () => ({
    service: "whitetransport",
    config,
    transport: transport.status()
  }));

  app.get("/", async () => ({
    service: "whitetransport",
    role: "transport-only",
    endpoints: {
      health: "/health",
      debugConfig: "/debug/config",
      wisp: config.wispPath
    }
  }));
}
