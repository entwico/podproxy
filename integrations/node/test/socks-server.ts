import net from 'node:net';

import {
  SOCKS_VERSION,
  SocksMethod,
  SocksReply,
  decodeConnectRequest,
  encodeMethodReply,
  encodeReply,
} from '../src/socks5';

export interface TestSocksServer {
  port: number;
  connections: () => number;
  requests: { host: string; port: number }[];
  close: () => Promise<void>;
}

// minimal SOCKS5 server for integration tests: accepts any hostname and always
// dials 127.0.0.1 on the requested port, so each backend is addressed by port
export function startSocksServer(): Promise<TestSocksServer> {
  let connections = 0;
  const requests: { host: string; port: number }[] = [];

  // allowHalfOpen on both hops so a client half-close still lets the response flow back
  const server = net.createServer({ allowHalfOpen: true }, (client) => {
    client.on('error', () => {});

    client.once('data', (greeting) => {
      if (greeting[0] !== SOCKS_VERSION) {
        client.destroy();

        return;
      }

      client.write(encodeMethodReply(SocksMethod.NoAuth));

      client.once('data', (request) => {
        const destination = decodeConnectRequest(request);

        if (!destination) {
          client.end(encodeReply(SocksReply.AddressTypeNotSupported));

          return;
        }

        requests.push(destination);

        const upstream = net.connect({ host: '127.0.0.1', port: destination.port, allowHalfOpen: true });

        upstream.on('error', () => client.destroy());

        upstream.on('connect', () => {
          connections++;
          client.write(encodeReply(SocksReply.Succeeded));
          client.pipe(upstream);
          upstream.pipe(client);
        });

        // abortive close on one side tears down the other; graceful close propagates via pipe
        upstream.on('close', (hadError) => {
          if (hadError) {
            client.destroy();
          }
        });

        client.on('close', (hadError) => {
          if (hadError) {
            upstream.destroy();
          }
        });
      });
    });
  });

  return new Promise((resolve, reject) => {
    server.on('error', reject);

    server.listen(0, '127.0.0.1', () => {
      const address = server.address() as net.AddressInfo;

      resolve({
        port: address.port,
        connections: () => connections,
        requests,
        close: () => new Promise<void>((res) => server.close(() => res())),
      });
    });
  });
}
