/* eslint-disable unicorn/no-this-outside-of-class -- prototype monkey-patches forward the caller's receiver */
import dns from 'node:dns';

import type { Logger } from './logger';

type ShouldProxy = (host: string) => boolean;
type GetFakeIp = (hostname: string) => string;

export interface PatchDnsOptions {
  shouldProxy: ShouldProxy;
  getFakeIp: GetFakeIp;
  logger: Logger;
}

export function patchDns({ shouldProxy, getFakeIp, logger }: PatchDnsOptions): void {
  patchLookup(shouldProxy, getFakeIp, logger);
  patchPromisesLookup(shouldProxy, getFakeIp, logger);
  patchResolve4(shouldProxy, getFakeIp, logger);
  patchResolve6(shouldProxy);
  patchPromisesResolve4(shouldProxy, getFakeIp);
  patchPromisesResolve6(shouldProxy);
  patchResolveSrv(shouldProxy);
  patchPromisesResolveSrv(shouldProxy);
  patchResolverPrototype(shouldProxy, getFakeIp);
  patchPromisesResolverPrototype(shouldProxy, getFakeIp);
}

function patchLookup(shouldProxy: ShouldProxy, getFakeIp: GetFakeIp, logger: Logger): void {
  const original = dns.lookup.bind(dns);

  dns.lookup = function (hostname: string, options: any, callback?: any) {
    if (typeof options === 'function') {
      callback = options;
      options = {};
    }

    if (shouldProxy(hostname)) {
      const fakeIp = getFakeIp(hostname);
      logger.debug(`dns.lookup ${hostname} → ${fakeIp}`);

      if (callback) {
        if (options && options.all) {
          queueMicrotask(() => callback(null, [{ address: fakeIp, family: 4 }]));
        } else {
          queueMicrotask(() => callback(null, fakeIp, 4));
        }
      }

      return;
    }

    return original(hostname, options, callback);
  } as typeof dns.lookup;
}

function patchPromisesLookup(shouldProxy: ShouldProxy, getFakeIp: GetFakeIp, logger: Logger): void {
  const original = dns.promises.lookup.bind(dns.promises);

  dns.promises.lookup = async function (hostname: string, options?: any) {
    if (shouldProxy(hostname)) {
      const fakeIp = getFakeIp(hostname);
      logger.debug(`dns.promises.lookup ${hostname} → ${fakeIp}`);

      if (options && options.all) {
        return [{ address: fakeIp, family: 4 }] as any;
      }

      return { address: fakeIp, family: 4 } as any;
    }

    return original(hostname, options);
  } as typeof dns.promises.lookup;
}

function patchResolve4(shouldProxy: ShouldProxy, getFakeIp: GetFakeIp, logger: Logger): void {
  const original = dns.resolve4.bind(dns);

  dns.resolve4 = function (hostname: string, options: any, callback?: any) {
    if (typeof options === 'function') {
      callback = options;
      options = {};
    }

    if (shouldProxy(hostname)) {
      const fakeIp = getFakeIp(hostname);
      logger.debug(`dns.resolve4 ${hostname} → ${fakeIp}`);

      if (callback) {
        queueMicrotask(() => callback(null, [fakeIp]));
      }

      return;
    }

    return original(hostname, options, callback);
  } as typeof dns.resolve4;
}

function patchResolve6(shouldProxy: ShouldProxy): void {
  const original = dns.resolve6.bind(dns);

  dns.resolve6 = function (hostname: string, options: any, callback?: any) {
    if (typeof options === 'function') {
      callback = options;
      options = {};
    }

    if (shouldProxy(hostname)) {
      if (callback) {
        queueMicrotask(() => callback(null, []));
      }

      return;
    }

    return original(hostname, options, callback);
  } as typeof dns.resolve6;
}

function patchPromisesResolve4(shouldProxy: ShouldProxy, getFakeIp: GetFakeIp): void {
  const original = dns.promises.resolve4.bind(dns.promises);

  dns.promises.resolve4 = async function (hostname: string, _options?: any) {
    if (shouldProxy(hostname)) {
      return [getFakeIp(hostname)] as any;
    }

    return original(hostname, _options);
  } as typeof dns.promises.resolve4;
}

function patchPromisesResolve6(shouldProxy: ShouldProxy): void {
  const original = dns.promises.resolve6.bind(dns.promises);

  dns.promises.resolve6 = async function (hostname: string, _options?: any) {
    if (shouldProxy(hostname)) {
      return [] as any;
    }

    return original(hostname, _options);
  } as typeof dns.promises.resolve6;
}

function patchResolveSrv(shouldProxy: ShouldProxy): void {
  const original = dns.resolveSrv.bind(dns);

  dns.resolveSrv = function (hostname: string, callback: any) {
    if (shouldProxy(hostname)) {
      if (callback) {
        queueMicrotask(() => callback(null, []));
      }

      return;
    }

    return original(hostname, callback);
  } as typeof dns.resolveSrv;
}

function patchPromisesResolveSrv(shouldProxy: ShouldProxy): void {
  const original = dns.promises.resolveSrv.bind(dns.promises);

  dns.promises.resolveSrv = async function (hostname: string) {
    if (shouldProxy(hostname)) {
      return [];
    }

    return original(hostname);
  } as typeof dns.promises.resolveSrv;
}

function patchResolverPrototype(shouldProxy: ShouldProxy, getFakeIp: GetFakeIp): void {
  const originalResolve4 = dns.Resolver.prototype.resolve4;
  const originalResolve6 = dns.Resolver.prototype.resolve6;

  dns.Resolver.prototype.resolve4 = function (this: dns.Resolver, hostname: string, options: any, callback?: any) {
    if (typeof options === 'function') {
      callback = options;
      options = {};
    }

    if (shouldProxy(hostname)) {
      const fakeIp = getFakeIp(hostname);

      if (callback) {
        queueMicrotask(() => callback(null, [fakeIp]));
      }

      return;
    }

    return originalResolve4.call(this, hostname, options, callback);
  } as typeof dns.Resolver.prototype.resolve4;

  dns.Resolver.prototype.resolve6 = function (this: dns.Resolver, hostname: string, options: any, callback?: any) {
    if (typeof options === 'function') {
      callback = options;
      options = {};
    }

    if (shouldProxy(hostname)) {
      if (callback) {
        queueMicrotask(() => callback(null, []));
      }

      return;
    }

    return originalResolve6.call(this, hostname, options, callback);
  } as typeof dns.Resolver.prototype.resolve6;
}

function patchPromisesResolverPrototype(shouldProxy: ShouldProxy, getFakeIp: GetFakeIp): void {
  const originalResolve4 = dns.promises.Resolver.prototype.resolve4;
  const originalResolve6 = dns.promises.Resolver.prototype.resolve6;

  dns.promises.Resolver.prototype.resolve4 = async function (
    this: dns.promises.Resolver,
    hostname: string,
    _options?: any,
  ) {
    if (shouldProxy(hostname)) {
      return [getFakeIp(hostname)] as any;
    }

    return originalResolve4.call(this, hostname, _options);
  } as typeof dns.promises.Resolver.prototype.resolve4;

  dns.promises.Resolver.prototype.resolve6 = async function (
    this: dns.promises.Resolver,
    hostname: string,
    _options?: any,
  ) {
    if (shouldProxy(hostname)) {
      return [] as any;
    }

    return originalResolve6.call(this, hostname, _options);
  } as typeof dns.promises.Resolver.prototype.resolve6;
}
