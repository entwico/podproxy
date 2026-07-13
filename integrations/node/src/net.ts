import fs from 'node:fs';
import net from 'node:net';

import type { Logger } from './logger';
import {
  SOCKS_VERSION,
  SocksAddressType,
  SocksMethod,
  SocksReply,
  encodeConnectRequest,
  encodeGreeting,
  replyMessage,
} from './socks5';

export interface PatchNetOptions {
  shouldProxy: (host: string) => boolean;
  fakeIpToHostname: Map<string, string>;
  proxy: { host: string; port: number };
  logger: Logger;
}

const HANDSHAKE_TIMEOUT_MS = 10_000;

interface QueuedWrite {
  chunk: any;
  encoding: BufferEncoding | undefined;
  cb: ((error?: Error | null) => void) | undefined;
}

export function patchNet({ shouldProxy, fakeIpToHostname, proxy, logger }: PatchNetOptions): void {
  const originalConnect = net.connect.bind(net);

  // returns a real net.Socket and runs the SOCKS5 handshake on it before revealing
  // it as connected. it must be a real socket (node wraps custom Duplexes in a
  // JSStreamSocket for http2/tls, losing ref/unref and racing teardown), and no byte
  // past the SOCKS reply may enter the JS buffer (http2 reads the native handle only)
  function createProxiedConnection(host: string, port: number, options: any, callback?: () => void): net.Socket {
    const originalHostname = fakeIpToHostname.get(host);
    const targetHost = originalHostname ?? host;

    logger.info(`${targetHost}:${port}`);

    const socket = new net.Socket({ allowHalfOpen: Boolean(options.allowHalfOpen) });

    let phase: 'setup' | 'open' | 'failed' = 'setup';
    let aborted = false;
    let closedDuringSetup = false;
    let queuedWrites: QueuedWrite[] = [];
    let queuedEnd: { chunk: any; encoding: BufferEncoding | undefined; cb: (() => void) | undefined } | null = null;
    const deferredListeners: { method: 'on' | 'once'; event: string; listener: (...args: any[]) => void }[] = [];

    const realWrite = socket.write.bind(socket);
    const realEmit = socket.emit.bind(socket);
    const realDestroy = socket.destroy.bind(socket);
    const realOn = socket.on.bind(socket);
    const realOnce = socket.once.bind(socket);
    const realRemoveListener = socket.removeListener.bind(socket);

    // never read during setup: bytes stay in the kernel until the consumer takes over
    socket.pause();

    if (options.noDelay) {
      socket.setNoDelay(true);
    }

    if (options.keepAlive) {
      socket.setKeepAlive(true, options.keepAliveInitialDelay ?? 0);
    }

    if (options.timeout) {
      socket.setTimeout(options.timeout);
    }

    // connect() resets this.write to the prototype, so it must run before the overrides
    socket.connect({ host: proxy.host, port: proxy.port });

    const restore = () => {
      delete (socket as any).emit;
      delete (socket as any).write;
      delete (socket as any).end;
      delete (socket as any).destroy;
      delete (socket as any).on;
      delete (socket as any).once;
      delete (socket as any).addListener;
      delete (socket as any).removeListener;
      delete (socket as any).off;
    };

    const fail = (err: Error) => {
      if (phase !== 'setup' || aborted) {
        return;
      }

      phase = 'failed';
      restore();
      logger.error(`SOCKS error for ${targetHost}:${port}: ${err.message}`);

      if (socket.destroyed) {
        realEmit('error', err);

        if (closedDuringSetup) {
          realEmit('close', true);
        }
      } else {
        realDestroy(err);
      }
    };

    // hold 'connect'/'ready' back until the handshake is done; 'data'/'end'
    // cannot fire during setup because the socket never reads
    (socket as any).emit = (event: string, ...args: any[]): boolean => {
      if (phase === 'setup' && !aborted) {
        switch (event) {
          case 'connect': {
            void establish();

            return false;
          }
          case 'ready': {
            return false;
          }
          case 'close': {
            closedDuringSetup = true;
            fail(new Error('connection closed during setup'));

            return false;
          }
          case 'error': {
            fail(args[0]);

            return false;
          }
        }
      }

      return realEmit(event, ...args);
    };

    (socket as any).write = (chunk: any, encoding?: any, cb?: any): boolean => {
      if (typeof encoding === 'function') {
        cb = encoding;
        encoding = undefined;
      }

      if (phase === 'setup') {
        queuedWrites.push({ chunk, encoding, cb });

        return false;
      }

      return realWrite(chunk, encoding, cb);
    };

    (socket as any).end = (chunk?: any, encoding?: any, cb?: any): net.Socket => {
      if (typeof chunk === 'function') {
        cb = chunk;
        chunk = undefined;
      } else if (typeof encoding === 'function') {
        cb = encoding;
        encoding = undefined;
      }

      if (phase === 'setup') {
        queuedEnd = { chunk, encoding, cb };

        return socket;
      }

      return (net.Socket.prototype.end as any).call(socket, chunk, encoding, cb);
    };

    (socket as any).destroy = (err?: Error): net.Socket => {
      if (phase === 'setup') {
        aborted = true;
      }

      return realDestroy(err);
    };

    // 'data'/'readable' listeners would start reads — attach them only once open
    const deferOrAttach = (method: 'on' | 'once', attach: (event: string, listener: any) => net.Socket) => {
      return (event: string, listener: any): net.Socket => {
        if (phase === 'setup' && (event === 'data' || event === 'readable')) {
          deferredListeners.push({ method, event, listener });

          return socket;
        }

        return attach(event, listener);
      };
    };

    (socket as any).on = deferOrAttach('on', realOn);
    (socket as any).addListener = (socket as any).on;
    (socket as any).once = deferOrAttach('once', realOnce);
    (socket as any).removeListener = (event: string, listener: any): net.Socket => {
      const index = deferredListeners.findIndex((entry) => entry.event === event && entry.listener === listener);

      if (index !== -1) {
        deferredListeners.splice(index, 1);

        return socket;
      }

      return realRemoveListener(event, listener);
    };
    (socket as any).off = (socket as any).removeListener;

    // report the target instead of the proxy address
    Object.defineProperties(socket, {
      remoteAddress: { configurable: true, get: () => host },
      remotePort: { configurable: true, get: () => port },
    });

    if (callback) {
      realOnce('connect', callback);
    }

    // read exactly n bytes from the fd, polling on EAGAIN; anything beyond n stays in the kernel
    const readExact = async (n: number): Promise<Buffer> => {
      const out = Buffer.alloc(n);
      const deadline = Date.now() + HANDSHAKE_TIMEOUT_MS;
      let offset = 0;

      while (offset < n) {
        if (aborted || socket.destroyed) {
          throw new Error('socket destroyed during SOCKS handshake');
        }

        const fd = (socket as any)._handle?.fd;

        if (typeof fd !== 'number' || fd < 0) {
          throw new Error('socket handle unavailable during SOCKS handshake');
        }

        let bytesRead: number;

        try {
          bytesRead = fs.readSync(fd, out, offset, n - offset, null);
        } catch (error: any) {
          if (error.code === 'EAGAIN' || error.code === 'EWOULDBLOCK') {
            if (Date.now() > deadline) {
              throw new Error('SOCKS handshake timed out', { cause: error });
            }

            await new Promise((resolve) => setTimeout(resolve, 1));

            continue;
          }

          throw error;
        }

        if (bytesRead === 0) {
          throw new Error('connection closed during SOCKS handshake');
        }

        offset += bytesRead;
      }

      return out;
    };

    const handshake = async (): Promise<void> => {
      realWrite(encodeGreeting());

      const method = await readExact(2);

      if (method[0] !== SOCKS_VERSION || method[1] !== SocksMethod.NoAuth) {
        throw new Error('SOCKS5 proxy requires authentication or sent an invalid method reply');
      }

      realWrite(encodeConnectRequest(targetHost, port));

      const reply = await readExact(4);

      if (reply[0] !== SOCKS_VERSION) {
        throw new Error('invalid SOCKS5 reply');
      }

      if (reply[1] !== SocksReply.Succeeded) {
        throw new Error(`SOCKS5 connect failed: ${replyMessage(reply[1])}`);
      }

      // skip the bound address, exact length per address type
      switch (reply[3]) {
        case SocksAddressType.IPv4: {
          await readExact(4 + 2);
          break;
        }
        case SocksAddressType.Domain: {
          const length = await readExact(1);

          await readExact(length[0] + 2);
          break;
        }
        case SocksAddressType.IPv6: {
          await readExact(16 + 2);
          break;
        }
        default: {
          throw new Error('invalid SOCKS5 reply address type');
        }
      }
    };

    const establish = async () => {
      try {
        await handshake();

        if (phase !== 'setup' || aborted || socket.destroyed) {
          return;
        }

        // reset flow state so the socket hands over like a fresh net.Socket;
        // reading may be stuck true from a consumer read() issued while connecting
        (socket as any)._readableState.flowing = null;
        (socket as any)._readableState.reading = false;

        phase = 'open';
        restore();

        for (const { method, event, listener } of deferredListeners) {
          socket[method](event as any, listener);
        }

        deferredListeners.length = 0;

        const writes = queuedWrites;

        queuedWrites = [];

        for (const { chunk, encoding, cb } of writes) {
          socket.write(chunk, encoding as any, cb);
        }

        if (queuedEnd) {
          (socket.end as any)(queuedEnd.chunk, queuedEnd.encoding, queuedEnd.cb);
          queuedEnd = null;
        }

        // the handle never started reading (the handshake used the fd) — start it
        // before 'connect' so native consumers like http2 do not miss bytes
        const handle = (socket as any)._handle;

        if (handle && !handle.reading) {
          handle.reading = true;
          handle.readStart();
        }

        socket.emit('connect');
        socket.emit('ready');

        if (writes.length > 0) {
          socket.emit('drain');
        }
      } catch (error) {
        fail(error instanceof Error ? error : new Error(String(error)));
      }
    };

    return socket;
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
      return createProxiedConnection(host, port, options, callback);
    }

    return originalConnect(options, callback);
  }

  net.connect = patchedConnect as typeof net.connect;
  net.createConnection = patchedConnect as typeof net.createConnection;
}
