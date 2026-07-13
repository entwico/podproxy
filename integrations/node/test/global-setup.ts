import { execFileSync } from 'node:child_process';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const nodeRoot = fileURLToPath(new URL('..', import.meta.url));

function reachable(port: number): Promise<boolean> {
  return new Promise((resolve) => {
    const probe = net.connect({ host: '127.0.0.1', port });

    probe.once('connect', () => {
      probe.destroy();
      resolve(true);
    });

    probe.once('error', () => resolve(false));
    probe.setTimeout(1000, () => {
      probe.destroy();
      resolve(false);
    });
  });
}

// mongodb for the integration tests: an explicit MONGODB_TEST_PORT or an already
// running instance wins, otherwise the compose service is started on 48717
export default async function setup(): Promise<void> {
  if (process.env.MONGODB_TEST_PORT) {
    return;
  }

  if ((await reachable(27_017)) || (await reachable(48_717))) {
    return;
  }

  try {
    execFileSync('docker', ['compose', 'up', '-d', '--wait', 'mongodb'], {
      cwd: nodeRoot,
      stdio: 'inherit',
      timeout: 120_000,
    });
  } catch {
    console.warn(`[global-setup] could not start mongodb via docker compose (${path.join(nodeRoot, 'docker-compose.yaml')}), mongodb tests will be skipped`);
  }
}
