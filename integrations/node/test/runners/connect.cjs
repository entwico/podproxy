const { createClient } = require('@connectrpc/connect');
const { createGrpcTransport, Http2SessionManager } = require('@connectrpc/connect-node');
const { EchoService, RESPONSE_PADDING } = require('../fixtures/echo.cjs');

const port = process.env.PODPROXY_TEST_PORT;

async function main() {
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
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
