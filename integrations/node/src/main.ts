import { createLogger, isLogLevel } from './logger';
import { loadPatterns, loadPatternsAsync } from './pac';
import { patchDns } from './dns';
import { patchNet } from './net';

const DEFAULT_SOCKS_PROXY = 'socks5://127.0.0.1:9080';
const DEFAULT_PAC_URL = 'http://127.0.0.1:9082';

const proxyUrl = process.env.DEV_SOCKS_PROXY ?? DEFAULT_SOCKS_PROXY;
const pacUrl = process.env.DEV_PROXY_PAC_URL ?? DEFAULT_PAC_URL;

export function main(pacLoader: typeof loadPatterns | typeof loadPatternsAsync) {
  if (proxyUrl) {
    const rawLevel = process.env.DEV_PROXY_LOG ?? 'error';

    if (!isLogLevel(rawLevel)) {
      console.error(`[dev-proxy] invalid DEV_PROXY_LOG="${rawLevel}", expected: error, info, debug`);
      process.exit(1);
    }

    const logger = createLogger(rawLevel);

    const url = new URL(proxyUrl);

    const proxy = {
      host: url.hostname,
      port: parseInt(url.port, 10),
      type: url.protocol === 'socks4:' ? 4 : 5,
    };

    const patterns = pacLoader({ match: process.env.DEV_PROXY_MATCH, pacUrl, logger });

    const processWithPatterns = (patterns: Awaited<ReturnType<typeof pacLoader>>) => {
      const bypassHosts = new Set(['localhost', '127.0.0.1', '::1']);

      function shouldProxy(host: string): boolean {
        if (!host || bypassHosts.has(host)) {
          return false;
        }

        return patterns.some((p) => p.test(host));
      }

      // fake IP range for proxied hosts (192.0.2.0/24 is TEST-NET-1)
      let fakeIpCounter = 1;
      const hostnameToFakeIp = new Map<string, string>();
      const fakeIpToHostname = new Map<string, string>();

      function getFakeIp(hostname: string): string {
        const existing = hostnameToFakeIp.get(hostname);

        if (existing) {
          return existing;
        }

        const fakeIp = `192.0.2.${fakeIpCounter++}`;
        hostnameToFakeIp.set(hostname, fakeIp);
        fakeIpToHostname.set(fakeIp, hostname);

        return fakeIp;
      }

      patchDns({ shouldProxy, getFakeIp, logger });
      patchNet({ shouldProxy, fakeIpToHostname, proxy, logger });

      logger.info(`SOCKS5 proxy enabled: ${proxy.host}:${proxy.port} (${patterns.length} patterns)`);
    };

    if (patterns instanceof Promise) {
      return patterns.then(processWithPatterns);
    }

    processWithPatterns(patterns);
  }
}