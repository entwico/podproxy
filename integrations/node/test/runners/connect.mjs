import { createClient } from '@connectrpc/connect';
import { Http2SessionManager, createGrpcTransport } from '@connectrpc/connect-node';
import fixtures from '../fixtures/echo.cjs';

const { EchoService, RESPONSE_PADDING } = fixtures;
const port = process.env.PODPROXY_TEST_PORT;
const baseUrl = `http://echo.podproxy-it.test:${port}`;

const sessionManager = new Http2SessionManager(baseUrl);

const transport = createGrpcTransport({
  baseUrl,
  sessionManager,
});

const client = createClient(EchoService, transport);
const response = await client.echo({ message: 'ping' });

sessionManager.abort();

if (response.message !== `echo:ping:${RESPONSE_PADDING}`) {
  console.error(`unexpected response: ${String(response.message).slice(0, 100)}`);
  process.exit(1);
}

console.log('OK');
