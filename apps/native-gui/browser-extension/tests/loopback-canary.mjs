import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { spawn } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const repositoryRoot = fileURLToPath(new URL("../../../../", import.meta.url));
const extensionRoot = new URL("../", import.meta.url);
const readExtension = (file) => readFile(new URL(file, extensionRoot), "utf8");
const binaryPath = process.env.WT_BROWSER_BRIDGE_BINARY || path.join(repositoryRoot, ".tmp", "cli", "wt-browser-credential-bridge");

await mkdir(path.join(repositoryRoot, ".tmp"), { recursive: true });
const evidenceDir = await mkdtemp(path.join(repositoryRoot, ".tmp", "mv3-loopback-canary-"));
const configDir = path.join(evidenceDir, "config");
await mkdir(configDir, { recursive: true });

const exportContext = { self: {}, URL, Date };
vm.runInNewContext(await readExtension("lib/export-format.js"), exportContext, { filename: "export-format.js" });
const browserExport = exportContext.self.WTBrowserExport;
const bridgeContext = { self: {} };
vm.runInNewContext(await readExtension("lib/bridge-client.js"), bridgeContext, { filename: "bridge-client.js" });
const bridge = bridgeContext.self.WTBrowserBridge;
const exported = browserExport.buildBrowserExport({
  cookies: [
    { name: "Session_id", value: "synthetic-canary-cookie", domain: ".yandex.ru", path: "/", secure: true, httpOnly: true },
    { name: "yandexuid", value: "synthetic-canary-device", domain: ".yandex.ru", path: "/", secure: true },
  ],
  sourceURL: "https://telemost.yandex.ru/j/synthetic-canary",
});

let child;
let stdout = "";
let stderr = "";
let receiptPath;
let bridgeURL;

try {
  await stat(binaryPath);
  child = spawn(binaryPath, [], {
    cwd: repositoryRoot,
    env: {
      ...process.env,
      WT_NATIVE_GUI_CONFIG_DIR: configDir,
      WT_NATIVE_GUI_FAKE_RUNTIME: "1",
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");

  let resolveReady;
  let rejectReady;
  const readyPromise = new Promise((resolve, reject) => {
    resolveReady = resolve;
    rejectReady = reject;
  });
  const exitPromise = new Promise((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code, signal) => {
      if (!stdout.includes("READY ")) {
        rejectReady(new Error("bridge exited before READY"));
      }
      resolve({ code, signal });
    });
  });
  child.stdout.on("data", (chunk) => {
    stdout += chunk;
    if (/^READY .*\n/m.test(stdout)) {
      resolveReady();
    }
  });
  child.stderr.on("data", (chunk) => {
    stderr += chunk;
  });
  const readyTimeout = new Promise((_, reject) => {
    setTimeout(() => reject(new Error("timed out waiting for bridge")), 5000);
  });
  await Promise.race([readyPromise, readyTimeout]);
  const pairing = bridge.parsePairing(stdout.split("\n").find(line => line.startsWith("READY ")));
  bridgeURL = pairing.origin + bridge.BRIDGE_PATH;

  const origin = "chrome-extension://abcdefghijklmnopabcdefghijklmnop";
  const unauthorizedProbe = await fetch(bridgeURL, { headers: { Origin: origin } });
  assert.equal(unauthorizedProbe.status, 403);
  const probe = await fetch(bridgeURL, {
    headers: { Origin: origin, "X-WT-Bridge-Nonce": pairing.nonce },
  });
  assert.equal(probe.status, 204);
  const preflight = await fetch(bridgeURL, {
    method: "OPTIONS",
    headers: {
      Origin: origin,
      "Access-Control-Request-Method": "POST",
      "Access-Control-Request-Headers": "content-type,x-wt-bridge-nonce",
    },
  });
  assert.equal(preflight.status, 204);
  assert.equal(preflight.headers.get("access-control-allow-origin"), origin);

  let postResponse;
  const fetchFromExtensionOrigin = async (url, options) => {
    postResponse = await fetch(url, {
      ...options,
      headers: { ...options.headers, Origin: origin },
    });
    return postResponse;
  };
  const summary = await bridge.sendBrowserExport(exported, {
    fetchImpl: fetchFromExtensionOrigin,
    pairing,
  });
  assert.deepEqual(summary.platforms, ["telemost"]);
  assert.equal(summary.count, 1);
  assert.equal(postResponse.headers.get("access-control-allow-origin"), origin);
  assert.doesNotMatch(JSON.stringify(summary), /synthetic-canary-(?:token|cookie|device)/);

  const exit = await Promise.race([
    exitPromise,
    new Promise((_, reject) => setTimeout(() => reject(new Error("bridge did not exit after one import")), 5000)),
  ]);
  assert.equal(exit.code, 0);

  const tokenPath = path.join(configDir, "client-tokens.json");
  const tokenFile = JSON.parse(await readFile(tokenPath, "utf8"));
  const tokenMode = (await stat(tokenPath)).mode & 0o777;
  assert.equal(tokenMode, 0o600);
  assert.equal(tokenFile.credentials.length, 1);
  assert.equal(tokenFile.credentials[0].platform, "telemost");
  assert.equal(tokenFile.credentials[0].token || "", "");
  assert.equal(tokenFile.credentials[0].extra, "https://telemost.yandex.ru/j/synthetic-canary");
  assert.match(tokenFile.credentials[0].cookie, /synthetic-canary-cookie/);
  assert.doesNotMatch(stdout + stderr, /synthetic-canary-(?:token|cookie|device)/);

  const receipt = {
    bridge: { url: bridgeURL, origin_checked: true, one_shot_exit: true },
    response: summary,
    consumer_readback: {
      credential_count: tokenFile.credentials.length,
      platforms: tokenFile.credentials.map((credential) => credential.platform),
      has_token: Boolean(tokenFile.credentials[0].token),
      has_cookie: Boolean(tokenFile.credentials[0].cookie),
      file_mode: "0600",
    },
    secrets_printed: false,
  };
  receiptPath = path.join(evidenceDir, "receipt.json");
  await writeFile(receiptPath, JSON.stringify(receipt, null, 2) + "\n", { mode: 0o600 });
  console.log("loopback canary passed: " + path.relative(repositoryRoot, receiptPath));
} catch (error) {
  console.error("loopback canary failed: " + error.message);
  process.exitCode = 1;
} finally {
  if (child && child.exitCode === null) {
    child.kill("SIGTERM");
  }
  await rm(configDir, { recursive: true, force: true });
}
