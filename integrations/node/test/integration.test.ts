import { execFile, execFileSync } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import http2 from 'node:http2';
import { createRequire } from 'node:module';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { connectNodeAdapter } from '@connectrpc/connect-node';
import * as grpc from '@grpc/grpc-js';
import { afterAll, beforeAll, describe, expect, it } from 'vitest';

import { type TestSocksServer, startSocksServer } from './socks-server';

const require_ = createRequire(import.meta.url);
const { EchoService, jsonEchoDefinition, RESPONSE_PADDING } = require_('./fixtures/echo.cjs');

const nodeRoot = fileURLToPath(new URL('..', import.meta.url));
const distDir = path.join(nodeRoot, 'dist');

const TEST_TIMEOUT = 60_000;
const CHILD_TIMEOUT = 30_000;

function probe(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const socket = net.connect({ host: '127.0.0.1', port });

    socket.once('connect', () => {
      socket.destroy();
      resolve(true);
    });

    socket.once('error', () => resolve(false));
    socket.setTimeout(1000, () => {
      socket.destroy();
      resolve(false);
    });
  });
}

// an explicit MONGODB_TEST_PORT wins; otherwise 27017 (CI service container,
// local mongod) and 48717 (docker-compose.yaml, started by test/global-setup.ts)
const mongoCandidates = process.env.MONGODB_TEST_PORT ? [Number(process.env.MONGODB_TEST_PORT)] : [27_017, 48_717];

let mongoPort = 0;

for (const candidate of mongoCandidates) {
  if (await probe(candidate)) {
    mongoPort = candidate;
    break;
  }
}

const mongoAvailable = mongoPort !== 0;

interface NodeRuntime {
  name: string;
  execPath: string;
  cwd: string;
  available: boolean;
}

// the launching proto shim pins PROTO_NODE_VERSION for children, which would
// override the per-directory .prototools — strip it
function childEnv(extra: Record<string, string>): Record<string, string | undefined> {
  const env: Record<string, string | undefined> = { ...process.env, NODE_OPTIONS: '', PROTO_AUTO_INSTALL: 'false', ...extra };

  delete env.PROTO_NODE_VERSION;

  return env;
}

// each subfolder of test/node-versions pins a node version via .prototools; the
// proto shim picks it from the child cwd. CI covers versions via its job matrix
function discoverRuntimes(): NodeRuntime[] {
  const protoShim = path.join(os.homedir(), '.proto', 'shims', 'node');
  const versionsDir = path.join(nodeRoot, 'test', 'node-versions');

  if (process.env.CI || !fs.existsSync(protoShim)) {
    return [{ name: `node ${process.versions.node}`, execPath: process.execPath, cwd: nodeRoot, available: true }];
  }

  return fs
    .readdirSync(versionsDir)
    .toSorted((a, b) => a.localeCompare(b))
    .map((dir) => {
      const cwd = path.join(versionsDir, dir);

      try {
        const version = execFileSync(protoShim, ['--version'], {
          cwd,
          timeout: 30_000,
          env: childEnv({}),
        })
          .toString()
          .trim();

        return { name: `node ${dir} (${version})`, execPath: protoShim, cwd, available: true };
      } catch {
        console.warn(`[integration] node ${dir} is not available via proto, its tests will be skipped`);

        return { name: `node ${dir} (unavailable)`, execPath: protoShim, cwd, available: false };
      }
    });
}

const runtimes = discoverRuntimes();

let socks: TestSocksServer;
let httpServer: http.Server;
let httpPort: number;
let connectServer: http2.Http2Server;
let connectPort: number;
let grpcServer: grpc.Server;
let grpcPort: number;

function listen(server: http.Server | http2.Http2Server): Promise<number> {
  return new Promise((resolve, reject) => {
    server.on('error', reject);
    server.listen(0, '127.0.0.1', () => resolve((server.address() as net.AddressInfo).port));
  });
}

interface RunResult {
  code: number;
  output: string;
}

function runClient(runtime: NodeRuntime, hookArgs: string[], runner: string, backendPort: number): Promise<RunResult> {
  return new Promise((resolve) => {
    execFile(
      runtime.execPath,
      [...hookArgs, path.join(nodeRoot, 'test', 'runners', runner)],
      {
        cwd: runtime.cwd,
        timeout: CHILD_TIMEOUT,
        env: childEnv({
          DEV_SOCKS_PROXY: `socks5://127.0.0.1:${socks.port}`,
          DEV_PROXY_PAC_URL: '',
          DEV_PROXY_MATCH: String.raw`\.podproxy-it\.test$`,
          DEV_PROXY_LOG: 'info',
          PODPROXY_TEST_PORT: String(backendPort),
        }),
      },
      (error, stdout, stderr) => {
        const code = error === null ? 0 : (typeof error.code === 'number' ? error.code : 1);
        const timedOut = error !== null && error.killed === true;

        resolve({ code, output: `${timedOut ? '(child timed out)\n' : ''}${stdout}${stderr}` });
      },
    );
  });
}

beforeAll(async () => {
  execFileSync(process.execPath, ['build.mjs'], { cwd: nodeRoot, env: { ...process.env, NODE_OPTIONS: '' } });

  socks = await startSocksServer();

  httpServer = http.createServer((_req, res) => {
    res.end(`hello from http backend:${RESPONSE_PADDING}`);
  });
  httpPort = await listen(httpServer);

  // this server closes the session right after responding, so the response arrives
  // with a close right behind it — buffered data must survive
  let connectSession: http2.ServerHttp2Session | undefined;

  connectServer = http2.createServer(
    connectNodeAdapter({
      routes: (router) => {
        router.service(EchoService, {
          echo: (req: any) => {
            connectSession?.close();

            return { message: `echo:${req.message}:${RESPONSE_PADDING}` };
          },
        } as any);
      },
    }),
  );
  connectServer.on('session', (session) => {
    connectSession = session;
  });
  connectPort = await listen(connectServer);

  grpcServer = new grpc.Server();
  grpcServer.addService(jsonEchoDefinition, {
    echo: (call: any, cb: (err: Error | null, res?: { message: string }) => void) => {
      cb(null, { message: `echo:${call.request.message}:${RESPONSE_PADDING}` });
    },
  });
  grpcPort = await new Promise<number>((resolve, reject) => {
    grpcServer.bindAsync('127.0.0.1:0', grpc.ServerCredentials.createInsecure(), (err, port) => {
      if (err) reject(err);
      else resolve(port);
    });
  });
}, TEST_TIMEOUT);

afterAll(async () => {
  grpcServer?.forceShutdown();
  connectServer?.close();
  httpServer?.close();
  await socks?.close();
});

const formats = [
  { name: 'esm', hookArgs: () => ['--import', path.join(distDir, 'proxy.mjs')], ext: 'mjs' },
  { name: 'cjs', hookArgs: () => ['--require', path.join(distDir, 'proxy.cjs')], ext: 'cjs' },
] as const;

describe.each(runtimes)('$name', (runtime) => {
  describe.each(formats)('$name hook', (format) => {
    it.skipIf(!runtime.available)(
      'proxies node:http requests',
      async () => {
        const before = socks.connections();
        const result = await runClient(runtime, format.hookArgs(), `http.${format.ext}`, httpPort);

        expect(result.output, result.output).toContain('OK');
        expect(result.code, result.output).toBe(0);
        expect(socks.connections()).toBeGreaterThan(before);
      },
      TEST_TIMEOUT,
    );

    it.skipIf(!runtime.available)(
      'proxies @grpc/grpc-js calls',
      async () => {
        const before = socks.connections();
        const result = await runClient(runtime, format.hookArgs(), `grpc.${format.ext}`, grpcPort);

        expect(result.output, result.output).toContain('OK');
        expect(result.code, result.output).toBe(0);
        expect(socks.connections()).toBeGreaterThan(before);
      },
      TEST_TIMEOUT,
    );

    it.skipIf(!runtime.available)(
      'proxies @connectrpc/connect-node calls',
      async () => {
        const before = socks.connections();
        const result = await runClient(runtime, format.hookArgs(), `connect.${format.ext}`, connectPort);

        expect(result.output, result.output).toContain('OK');
        expect(result.code, result.output).toBe(0);
        expect(socks.connections()).toBeGreaterThan(before);
      },
      TEST_TIMEOUT,
    );

    it.skipIf(!runtime.available || !mongoAvailable)(
      'proxies mongodb driver operations',
      async () => {
        const before = socks.connections();
        const result = await runClient(runtime, format.hookArgs(), `mongodb.${format.ext}`, mongoPort);

        expect(result.output, result.output).toContain('OK');
        expect(result.code, result.output).toBe(0);
        expect(socks.connections()).toBeGreaterThan(before);
      },
      TEST_TIMEOUT,
    );
  });
});
