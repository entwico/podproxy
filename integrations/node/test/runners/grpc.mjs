import grpc from '@grpc/grpc-js';
import fixtures from '../fixtures/echo.cjs';

const { jsonEchoDefinition, RESPONSE_PADDING } = fixtures;
const port = process.env.PODPROXY_TEST_PORT;

const client = new grpc.Client(`echo.podproxy-it.test:${port}`, grpc.credentials.createInsecure());
const method = jsonEchoDefinition.echo;

const response = await new Promise((resolve, reject) => {
  client.makeUnaryRequest(method.path, method.requestSerialize, method.responseDeserialize, { message: 'ping' }, (err, res) => {
    if (err) reject(err);
    else resolve(res);
  });
});

client.close();

if (response.message !== `echo:ping:${RESPONSE_PADDING}`) {
  console.error(`unexpected response: ${String(response.message).slice(0, 100)}`);
  process.exit(1);
}

console.log('OK');
