import net from 'net';
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';

import { startSocksServer, type TestSocksServer } from '../test/socks-server';
import { createLogger } from './logger';
import { patchNet } from './net';

const logger = createLogger('error');

const BIG_PAYLOAD = Buffer.alloc(256 * 1024, 'x');

function waitForConnect(socket: net.Socket): Promise<void> {
  return new Promise((resolve) => socket.once('connect', resolve));
}

function collect(socket: net.Socket): Promise<Buffer> {
  return new Promise((resolve, reject) => {
    const chunks: Buffer[] = [];

    socket.on('data', (chunk) => chunks.push(chunk));
    socket.on('end', () => resolve(Buffer.concat(chunks)));
    socket.on('error', reject);
  });
}

describe('patchNet', () => {
  const originalConnect = net.connect;
  const originalCreateConnection = net.createConnection;

  const fakeIpToHostname = new Map<string, string>();

  let socks: TestSocksServer;
  let echoServer: net.Server;
  let echoPort: number;
  let burstServer: net.Server;
  let burstPort: number;

  beforeAll(async () => {
    socks = await startSocksServer();

    echoServer = net.createServer((connection) => {
      connection.on('error', () => {});
      connection.pipe(connection);
    });

    burstServer = net.createServer((connection) => {
      connection.on('error', () => {});
      // write a large payload and close immediately: buffered data must survive
      connection.end(BIG_PAYLOAD);
    });

    [echoPort, burstPort] = await Promise.all(
      [echoServer, burstServer].map(
        (server) =>
          new Promise<number>((resolve) => {
            server.listen(0, '127.0.0.1', () => resolve((server.address() as net.AddressInfo).port));
          }),
      ),
    );
  });

  afterAll(async () => {
    echoServer.close();
    burstServer.close();
    await socks.close();
  });

  beforeEach(() => {
    fakeIpToHostname.clear();
    fakeIpToHostname.set('192.0.2.1', 'api.proxied.dev');

    patchNet({
      shouldProxy: (host) => host.endsWith('.proxied.dev'),
      fakeIpToHostname,
      proxy: { host: '127.0.0.1', port: socks.port },
      logger,
    });
  });

  afterEach(() => {
    net.connect = originalConnect;
    net.createConnection = originalCreateConnection;
    vi.restoreAllMocks();
  });

  it('returns a real net.Socket for proxied hosts', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort });

    expect(socket).toBeInstanceOf(net.Socket);
    expect(socket.connecting).toBe(true);

    await waitForConnect(socket);

    socket.destroy();
  });

  it('routes connections to matching hostnames through SOCKS', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort });

    await waitForConnect(socket);

    expect(socks.requests.at(-1)).toEqual({ host: 'svc.proxied.dev', port: echoPort });

    socket.destroy();
  });

  it('resolves fake IPs back to the original hostname', async () => {
    const socket = net.connect({ host: '192.0.2.1', port: echoPort });

    await waitForConnect(socket);

    expect(socks.requests.at(-1)).toEqual({ host: 'api.proxied.dev', port: echoPort });

    socket.destroy();
  });

  it('does not route non-proxied connections through SOCKS', async () => {
    const before = socks.requests.length;
    const socket = net.connect({ host: '127.0.0.1', port: echoPort });

    await waitForConnect(socket);

    expect(socks.requests.length).toBe(before);

    socket.destroy();
  });

  it('handles port+host argument form', async () => {
    const socket = net.connect(echoPort, 'api.proxied.dev');

    await waitForConnect(socket);

    expect(socks.requests.at(-1)).toEqual({ host: 'api.proxied.dev', port: echoPort });

    socket.destroy();
  });

  it('patches both net.connect and net.createConnection', () => {
    expect(net.connect).toBe(net.createConnection);
  });

  it('invokes the connect callback and emits ready', async () => {
    const onConnect = vi.fn();
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort }, onConnect);
    const onReady = new Promise((resolve) => socket.once('ready', resolve));

    await waitForConnect(socket);
    await onReady;

    expect(onConnect).toHaveBeenCalledOnce();
    expect(socket.connecting).toBe(false);

    socket.destroy();
  });

  it('reports the logical destination as remote address', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort });

    await waitForConnect(socket);

    expect(socket.remoteAddress).toBe('svc.proxied.dev');
    expect(socket.remotePort).toBe(echoPort);

    socket.destroy();
  });

  it('round-trips data through the echo backend', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort });

    await waitForConnect(socket);

    socket.end('hello through socks');

    const received = await collect(socket);

    expect(received.toString()).toBe('hello through socks');
  });

  it('holds back writes issued before the handshake completes', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort });

    socket.write('queued ');
    socket.end('writes');

    const received = await collect(socket);

    expect(received.toString()).toBe('queued writes');
  });

  it('delivers buffered data to paused-mode readers when the server closes immediately', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: burstPort });

    const chunks: Buffer[] = [];

    for await (const chunk of socket) {
      chunks.push(chunk);
    }

    expect(Buffer.concat(chunks).length).toBe(BIG_PAYLOAD.length);
  });

  it('supports native socket methods before the connection is established', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort });

    socket.unref();
    socket.ref();
    socket.setNoDelay(true);
    socket.setKeepAlive(true, 1000);
    socket.setTimeout(30_000);

    await waitForConnect(socket);

    socket.end('still works');

    const received = await collect(socket);

    expect(received.toString()).toBe('still works');
  });

  it('emits timeout on idle connections', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort });

    await waitForConnect(socket);

    const onTimeout = new Promise((resolve) => socket.setTimeout(50, () => resolve(true)));

    expect(await onTimeout).toBe(true);

    socket.destroy();
  });

  it('emits an error when the proxied destination is unreachable', async () => {
    let deadPort: number;

    {
      const probe = net.createServer();

      await new Promise<void>((resolve) => probe.listen(0, '127.0.0.1', () => resolve()));
      deadPort = (probe.address() as net.AddressInfo).port;
      await new Promise<void>((resolve) => probe.close(() => resolve()));
    }

    const socket = net.connect({ host: 'svc.proxied.dev', port: deadPort });
    const error = await new Promise<Error>((resolve) => socket.once('error', resolve));

    expect(error).toBeInstanceOf(Error);
    expect(socket.destroyed).toBe(true);
  });

  it('supports destroying the socket before the handshake completes', async () => {
    const socket = net.connect({ host: 'svc.proxied.dev', port: echoPort });
    const onClose = new Promise((resolve) => socket.once('close', resolve));

    socket.on('error', () => {});
    socket.destroy();

    await onClose;

    expect(socket.destroyed).toBe(true);
  });
});
