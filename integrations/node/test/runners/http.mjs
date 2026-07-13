import http from 'node:http';

const port = process.env.PODPROXY_TEST_PORT;

const body = await new Promise((resolve, reject) => {
  const req = http.get(`http://echo.podproxy-it.test:${port}/`, (res) => {
    const chunks = [];

    res.on('data', (chunk) => chunks.push(chunk));
    res.on('end', () => resolve(Buffer.concat(chunks).toString()));
    res.on('error', reject);
  });

  req.on('error', reject);
});

if (!body.startsWith('hello from http backend:')) {
  console.error(`unexpected body: ${body.slice(0, 100)}`);
  process.exit(1);
}

console.log('OK');
