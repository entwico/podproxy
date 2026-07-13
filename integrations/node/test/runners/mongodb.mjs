import { MongoClient } from 'mongodb';

const port = process.env.PODPROXY_TEST_PORT;

const client = new MongoClient(`mongodb://mongo.podproxy-it.test:${port}/?directConnection=true`, {
  serverSelectionTimeoutMS: 10000,
});

await client.connect();

const db = client.db('podproxy_it');
const ping = await db.command({ ping: 1 });

if (ping.ok !== 1) {
  console.error(`ping failed: ${JSON.stringify(ping)}`);
  process.exit(1);
}

const collection = db.collection('smoke_esm');

await collection.deleteMany({});
await collection.insertOne({ probe: 'podproxy' });

const doc = await collection.findOne({ probe: 'podproxy' });

if (!doc) {
  console.error('document roundtrip failed');
  process.exit(1);
}

await client.close();

console.log('OK');
