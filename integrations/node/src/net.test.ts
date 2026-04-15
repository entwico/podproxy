import net from 'net';
import { SocksClient } from 'socks';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createLogger } from './logger';
import { patchNet } from './net';

vi.mock('socks', () => ({
  SocksClient: {
    createConnection: vi.fn(),
  },
}));

const logger = createLogger('error');

function createMockSocksSocket() {
  const socket = new net.Socket();
  return socket;
}

describe('patchNet', () => {
  const originalConnect = net.connect;
  const originalCreateConnection = net.createConnection;

  const fakeIpToHostname = new Map<string, string>();

  beforeEach(() => {
    fakeIpToHostname.clear();
    fakeIpToHostname.set('192.0.2.1', 'api.proxied.dev');

    vi.mocked(SocksClient.createConnection).mockResolvedValue({
      socket: createMockSocksSocket(),
    } as any);

    patchNet({
      shouldProxy: (host) => host.endsWith('.proxied.dev'),
      fakeIpToHostname,
      proxy: { host: '127.0.0.1', port: 9080, type: 5 },
      logger,
    });
  });

  afterEach(() => {
    net.connect = originalConnect;
    net.createConnection = originalCreateConnection;
    vi.restoreAllMocks();
  });

  it('routes connections to fake IPs through SOCKS', () => {
    net.connect({ host: '192.0.2.1', port: 8080 });

    expect(SocksClient.createConnection).toHaveBeenCalledWith({
      proxy: { host: '127.0.0.1', port: 9080, type: 5 },
      command: 'connect',
      destination: { host: 'api.proxied.dev', port: 8080 },
    });
  });

  it('routes connections to matching hostnames through SOCKS', () => {
    net.connect({ host: 'svc.proxied.dev', port: 443 });

    expect(SocksClient.createConnection).toHaveBeenCalledWith({
      proxy: { host: '127.0.0.1', port: 9080, type: 5 },
      command: 'connect',
      destination: { host: 'svc.proxied.dev', port: 443 },
    });
  });

  it('does not route non-proxied connections through SOCKS', () => {
    const socket = net.connect({ host: '127.0.0.1', port: 3000 });
    socket.on('error', () => {}); // suppress ECONNREFUSED
    socket.destroy();
    expect(SocksClient.createConnection).not.toHaveBeenCalled();
  });

  it('handles port+host argument form', () => {
    net.connect(8080, 'api.proxied.dev');

    expect(SocksClient.createConnection).toHaveBeenCalledWith(
      expect.objectContaining({
        destination: { host: 'api.proxied.dev', port: 8080 },
      }),
    );
  });

  it('patches both net.connect and net.createConnection', () => {
    expect(net.connect).toBe(net.createConnection);
  });
});
