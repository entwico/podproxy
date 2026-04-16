import { execFileSync } from 'child_process';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { createLogger } from './logger';
import { loadPatterns, loadPatternsAsync, parsePacPatterns } from './pac';

vi.mock('child_process', () => ({
  execFileSync: vi.fn(),
}));

const mockedExecFileSync = vi.mocked(execFileSync);
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

describe('loadPatterns (sync)', () => {
  it('creates regex from match string', () => {
    const patterns = loadPatterns({ match: '\\.example\\.com$', logger });

    expect(patterns).toHaveLength(1);
    expect(patterns[0].test('api.example.com')).toBe(true);
    expect(patterns[0].test('other.dev')).toBe(false);
  });

  it('loads patterns from PAC URL via curl', () => {
    mockedExecFileSync.mockReturnValue('if (shExpMatch(host, "*.cluster.local")) return "SOCKS5 127.0.0.1:9080";');

    const patterns = loadPatterns({ pacUrl: 'http://localhost:9082', logger });

    expect(patterns).toHaveLength(1);
    expect(patterns[0].test('svc.cluster.local')).toBe(true);
    expect(mockedExecFileSync).toHaveBeenCalledWith('curl', ['-fsSL', '--max-time', '5', 'http://localhost:9082'], { encoding: 'utf-8' });
  });

  it('handles curl error gracefully', () => {
    mockedExecFileSync.mockImplementation(() => {
      throw new Error('connection refused');
    });

    const patterns = loadPatterns({ pacUrl: 'http://localhost:9082', logger });
    expect(patterns).toHaveLength(0);
  });

  it('returns empty array when no options provided', () => {
    const patterns = loadPatterns({ logger });
    expect(patterns).toHaveLength(0);
  });
});

describe('loadPatternsAsync', () => {
  it('creates regex from match string', async () => {
    const patterns = await loadPatternsAsync({ match: '\\.example\\.com$', logger });

    expect(patterns).toHaveLength(1);
    expect(patterns[0].test('api.example.com')).toBe(true);
    expect(patterns[0].test('other.dev')).toBe(false);
  });

  it('loads patterns from PAC URL via fetch', async () => {
    mockFetch('if (shExpMatch(host, "*.cluster.local")) return "SOCKS5 127.0.0.1:9080";');

    const patterns = await loadPatternsAsync({ pacUrl: 'http://localhost:9082', logger });

    expect(patterns).toHaveLength(1);
    expect(patterns[0].test('svc.cluster.local')).toBe(true);
    expect(fetch).toHaveBeenCalledWith('http://localhost:9082');
  });

  it('combines match and PAC patterns', async () => {
    mockFetch('shExpMatch(host, "*.pac.dev")');

    const patterns = await loadPatternsAsync({
      match: '\\.manual\\.dev$',
      pacUrl: 'http://localhost:9082',
      logger,
    });

    expect(patterns).toHaveLength(2);
    expect(patterns[0].test('api.manual.dev')).toBe(true);
    expect(patterns[1].test('svc.pac.dev')).toBe(true);
  });

  it('handles fetch error gracefully', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('connection refused')));

    const patterns = await loadPatternsAsync({ pacUrl: 'http://localhost:9082', logger });
    expect(patterns).toHaveLength(0);
  });

  it('returns empty array when no options provided', async () => {
    const patterns = await loadPatternsAsync({ logger });
    expect(patterns).toHaveLength(0);
  });
});
