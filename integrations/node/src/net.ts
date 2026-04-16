import { Duplex } from 'stream';
import net from 'net';
import { SocksClient } from 'socks';

import type { Logger } from './logger';

export interface PatchNetOptions {
  shouldProxy: (host: string) => boolean;
  fakeIpToHostname: Map<string, string>;
  proxy: { host: string; port: number; type: number };
  logger: Logger;
}

export function patchNet({ shouldProxy, fakeIpToHostname, proxy, logger }: PatchNetOptions): void {
  const originalConnect = net.connect.bind(net);

  function createProxiedConnection(host: string, port: number, callback?: () => void): Duplex {
    const originalHostname = fakeIpToHostname.get(host);
    const targetHost = originalHostname ?? host;

    if (!originalHostname && !shouldProxy(host)) {
      return originalConnect({ host, port }, callback);
    }

    logger.info(`${targetHost}:${port}`);

    let socksSocketRef: net.Socket | null = null;
    let pendingWrites: { chunk: any; encoding: BufferEncoding; cb: (error?: Error | null) => void }[] = [];

    const wrapper = new Duplex({
      read() {
        // data is pushed from socksSocket 'data' event below
      },
      write(chunk, encoding, cb) {
        if (socksSocketRef) {
          socksSocketRef.write(chunk, encoding, cb);
        } else {
          pendingWrites.push({ chunk, encoding, cb });
        }
      },
    });

    // grpc-js and other libraries expect net.Socket methods on the connection
    (wrapper as any).connecting = true;
    (wrapper as any).setNoDelay = (noDelay?: boolean) => { socksSocketRef?.setNoDelay(noDelay); return wrapper; };
    (wrapper as any).setKeepAlive = (enable?: boolean, delay?: number) => { socksSocketRef?.setKeepAlive(enable, delay); return wrapper; };
    (wrapper as any).ref = () => { socksSocketRef?.ref(); return wrapper; };
    (wrapper as any).unref = () => { socksSocketRef?.unref(); return wrapper; };
    (wrapper as any).remoteAddress = host;
    (wrapper as any).remotePort = port;

    SocksClient.createConnection({
      proxy: { host: proxy.host, port: proxy.port, type: proxy.type as 4 | 5 },
      command: 'connect',
      destination: { host: targetHost, port },
    })
      .then((info) => {
        socksSocketRef = info.socket;
        socksSocketRef.removeAllListeners();

        socksSocketRef.on('data', (data) => wrapper.push(data));
        socksSocketRef.on('end', () => wrapper.push(null));
        socksSocketRef.on('error', (err) => wrapper.destroy(err));
        socksSocketRef.on('close', () => { if (!wrapper.destroyed) wrapper.destroy(); });

        // flush writes that arrived before SOCKS connection was ready
        for (const { chunk, encoding, cb } of pendingWrites) {
          socksSocketRef.write(chunk, encoding, cb);
        }

        pendingWrites = [];

        (wrapper as any).connecting = false;
        wrapper.emit('connect');

        if (callback) {
          callback();
        }
      })
      .catch((err) => {
        logger.error(`SOCKS error for ${targetHost}:${port}: ${err.message}`);
        wrapper.destroy(err);
      });

    return wrapper;
  }

  function patchedConnect(...args: any[]): any {
    let options: any = {};
    let callback: (() => void) | undefined;

    if (typeof args[0] === 'object') {
      options = args[0];
      callback = args[1];
    } else if (typeof args[0] === 'number') {
      options.port = args[0];

      if (typeof args[1] === 'string') {
        options.host = args[1];
        callback = args[2];
      } else {
        callback = args[1];
      }
    } else {
      return originalConnect(args[0], args[1]);
    }

    const host = options.host ?? options.hostname ?? '127.0.0.1';
    const port = options.port;
    const originalHostname = fakeIpToHostname.get(host);

    if (originalHostname || shouldProxy(host)) {
      return createProxiedConnection(host, port, callback);
    }

    return originalConnect(options, callback);
  }

  net.connect = patchedConnect as typeof net.connect;
  net.createConnection = patchedConnect as typeof net.createConnection;
}
