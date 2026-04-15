import dns from 'dns';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { createLogger } from './logger';
import { patchDns } from './dns';

const logger = createLogger('error');

function callbackToPromise<T>(fn: (cb: (err: Error | null, ...args: T[]) => void) => void): Promise<T[]> {
  return new Promise((resolve, reject) => {
    fn((err, ...args) => {
      if (err) reject(err);
      else resolve(args);
    });
  });
}

describe('patchDns', () => {
  const originals = {
    lookup: dns.lookup,
    promisesLookup: dns.promises.lookup,
    resolve4: dns.resolve4,
    resolve6: dns.resolve6,
    promisesResolve4: dns.promises.resolve4,
    promisesResolve6: dns.promises.resolve6,
    resolveSrv: dns.resolveSrv,
    promisesResolveSrv: dns.promises.resolveSrv,
    resolverResolve4: dns.Resolver.prototype.resolve4,
    resolverResolve6: dns.Resolver.prototype.resolve6,
    promisesResolverResolve4: dns.promises.Resolver.prototype.resolve4,
    promisesResolverResolve6: dns.promises.Resolver.prototype.resolve6,
  };

  const getFakeIp = (hostname: string) => `192.0.2.${hostname.length}`;

  beforeEach(() => {
    patchDns({
      shouldProxy: (host) => host.endsWith('.proxied.dev'),
      getFakeIp,
      logger,
    });
  });

  afterEach(() => {
    dns.lookup = originals.lookup;
    dns.promises.lookup = originals.promisesLookup;
    dns.resolve4 = originals.resolve4;
    dns.resolve6 = originals.resolve6;
    dns.promises.resolve4 = originals.promisesResolve4;
    dns.promises.resolve6 = originals.promisesResolve6;
    dns.resolveSrv = originals.resolveSrv;
    dns.promises.resolveSrv = originals.promisesResolveSrv;
    dns.Resolver.prototype.resolve4 = originals.resolverResolve4;
    dns.Resolver.prototype.resolve6 = originals.resolverResolve6;
    dns.promises.Resolver.prototype.resolve4 = originals.promisesResolverResolve4;
    dns.promises.Resolver.prototype.resolve6 = originals.promisesResolverResolve6;
  });

  describe('dns.lookup', () => {
    it('returns fake IP for proxied host', async () => {
      const [address, family] = await callbackToPromise<any>((cb) => dns.lookup('api.proxied.dev', cb));
      expect(address).toBe(getFakeIp('api.proxied.dev'));
      expect(family).toBe(4);
    });

    it('returns fake IP with options argument', async () => {
      const [address, family] = await callbackToPromise<any>((cb) => dns.lookup('api.proxied.dev', {}, cb));
      expect(address).toBe(getFakeIp('api.proxied.dev'));
      expect(family).toBe(4);
    });
  });

  describe('dns.promises.lookup', () => {
    it('returns fake IP for proxied host', async () => {
      const result = await dns.promises.lookup('svc.proxied.dev');
      expect(result).toEqual({ address: getFakeIp('svc.proxied.dev'), family: 4 });
    });

    it('returns array when all option is set', async () => {
      const result = await dns.promises.lookup('svc.proxied.dev', { all: true });
      expect(result).toEqual([{ address: getFakeIp('svc.proxied.dev'), family: 4 }]);
    });
  });

  describe('dns.resolve4', () => {
    it('returns fake IP for proxied host', async () => {
      const [addresses] = await callbackToPromise<string[]>((cb) => dns.resolve4('api.proxied.dev', cb));
      expect(addresses).toEqual([getFakeIp('api.proxied.dev')]);
    });
  });

  describe('dns.resolve6', () => {
    it('returns empty array for proxied host', async () => {
      const [addresses] = await callbackToPromise<string[]>((cb) => dns.resolve6('api.proxied.dev', cb));
      expect(addresses).toEqual([]);
    });
  });

  describe('dns.promises.resolve4', () => {
    it('returns fake IP for proxied host', async () => {
      const result = await dns.promises.resolve4('api.proxied.dev');
      expect(result).toEqual([getFakeIp('api.proxied.dev')]);
    });
  });

  describe('dns.promises.resolve6', () => {
    it('returns empty array for proxied host', async () => {
      const result = await dns.promises.resolve6('api.proxied.dev');
      expect(result).toEqual([]);
    });
  });

  describe('dns.resolveSrv', () => {
    it('returns empty array for proxied host', async () => {
      const [addresses] = await callbackToPromise<any>((cb) => dns.resolveSrv('api.proxied.dev', cb));
      expect(addresses).toEqual([]);
    });
  });

  describe('dns.promises.resolveSrv', () => {
    it('returns empty array for proxied host', async () => {
      const result = await dns.promises.resolveSrv('api.proxied.dev');
      expect(result).toEqual([]);
    });
  });

  describe('Resolver prototype', () => {
    it('resolve4 returns fake IP for proxied host', async () => {
      const resolver = new dns.Resolver();
      const [addresses] = await callbackToPromise<string[]>((cb) => resolver.resolve4('api.proxied.dev', cb));
      expect(addresses).toEqual([getFakeIp('api.proxied.dev')]);
    });

    it('resolve6 returns empty array for proxied host', async () => {
      const resolver = new dns.Resolver();
      const [addresses] = await callbackToPromise<string[]>((cb) => resolver.resolve6('api.proxied.dev', cb));
      expect(addresses).toEqual([]);
    });
  });

  describe('promises.Resolver prototype', () => {
    it('resolve4 returns fake IP for proxied host', async () => {
      const resolver = new dns.promises.Resolver();
      const result = await resolver.resolve4('api.proxied.dev');
      expect(result).toEqual([getFakeIp('api.proxied.dev')]);
    });

    it('resolve6 returns empty array for proxied host', async () => {
      const resolver = new dns.promises.Resolver();
      const result = await resolver.resolve6('api.proxied.dev');
      expect(result).toEqual([]);
    });
  });
});
