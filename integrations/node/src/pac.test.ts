import { afterEach, describe, expect, it, vi } from 'vitest';

import { createLogger } from './logger';
import { loadPatterns, parsePacPatterns } from './pac';

const logger = createLogger('error');

function mockFetch(body: string) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ text: () => Promise.resolve(body) }));
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe('parsePacPatterns', () => {
  it('extracts shExpMatch patterns from PAC content', () => {
    const pac = `
      function FindProxyForURL(url, host) {
        if (shExpMatch(host, "*.example.com")) return "SOCKS5 127.0.0.1:9080";
        if (shExpMatch(host, "*.internal.dev")) return "SOCKS5 127.0.0.1:9080";
        return "DIRECT";
      }
    `;

    const patterns = parsePacPatterns(pac);

    expect(patterns).toHaveLength(2);
    expect(patterns[0].test('api.example.com')).toBe(true);
    expect(patterns[0].test('example.com')).toBe(false);
    expect(patterns[1].test('svc.internal.dev')).toBe(true);
  });

  it('escapes special regex characters in domain names', () => {
    const pac = 'shExpMatch(host, "*.my-app.io")';
    const patterns = parsePacPatterns(pac);

    expect(patterns).toHaveLength(1);
    expect(patterns[0].test('api.my-app.io')).toBe(true);
    expect(patterns[0].test('api.my-apXio')).toBe(false);
  });

  it('returns empty array for PAC without shExpMatch', () => {
    const patterns = parsePacPatterns('function FindProxyForURL() { return "DIRECT"; }');
    expect(patterns).toHaveLength(0);
  });
});

describe('loadPatterns', () => {
  it('creates regex from match string', async () => {
    const patterns = await loadPatterns({ match: '\\.example\\.com$', logger });

    expect(patterns).toHaveLength(1);
    expect(patterns[0].test('api.example.com')).toBe(true);
    expect(patterns[0].test('other.dev')).toBe(false);
  });

  it('loads patterns from PAC URL', async () => {
    mockFetch('if (shExpMatch(host, "*.cluster.local")) return "SOCKS5 127.0.0.1:9080";');

    const patterns = await loadPatterns({ pacUrl: 'http://localhost:9082', logger });

    expect(patterns).toHaveLength(1);
    expect(patterns[0].test('svc.cluster.local')).toBe(true);
    expect(fetch).toHaveBeenCalledWith('http://localhost:9082');
  });

  it('combines match and PAC patterns', async () => {
    mockFetch('shExpMatch(host, "*.pac.dev")');

    const patterns = await loadPatterns({
      match: '\\.manual\\.dev$',
      pacUrl: 'http://localhost:9082',
      logger,
    });

    expect(patterns).toHaveLength(2);
    expect(patterns[0].test('api.manual.dev')).toBe(true);
    expect(patterns[1].test('svc.pac.dev')).toBe(true);
  });

  it('handles PAC fetch error gracefully', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('connection refused')));

    const patterns = await loadPatterns({ pacUrl: 'http://localhost:9082', logger });
    expect(patterns).toHaveLength(0);
  });

  it('returns empty array when no options provided', async () => {
    const patterns = await loadPatterns({ logger });
    expect(patterns).toHaveLength(0);
  });
});
