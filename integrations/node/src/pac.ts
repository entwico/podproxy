import { execFileSync } from 'child_process';

import type { Logger } from './logger';

export interface LoadPatternsOptions {
  match?: string | undefined;
  pacUrl?: string | undefined;
  logger: Logger;
}

export function loadPatterns({ match, pacUrl, logger }: LoadPatternsOptions): RegExp[] {
  const patterns: RegExp[] = [];

  if (match) {
    patterns.push(new RegExp(match));
  }

  if (pacUrl) {
    try {
      const pac = execFileSync('curl', ['-fsSL', '--max-time', '5', pacUrl], { encoding: 'utf-8' });
      const parsed = parsePacPatterns(pac);
      patterns.push(...parsed);
      logger.debug(`loaded ${patterns.length} patterns from PAC`);
    } catch (err) {
      logger.error(`failed to load PAC from ${pacUrl}: ${(err as Error).message}`);
    }
  }

  return patterns;
}

export async function loadPatternsAsync({ match, pacUrl, logger }: LoadPatternsOptions): Promise<RegExp[]> {
  const patterns: RegExp[] = [];

  if (match) {
    patterns.push(new RegExp(match));
  }

  if (pacUrl) {
    try {
      const res = await fetch(pacUrl);
      const pac = await res.text();
      const parsed = parsePacPatterns(pac);
      patterns.push(...parsed);
      logger.debug(`loaded ${patterns.length} patterns from PAC`);
    } catch (err) {
      logger.error(`failed to load PAC from ${pacUrl}: ${(err as Error).message}`);
    }
  }

  return patterns;
}

export function parsePacPatterns(pac: string): RegExp[] {
  const patterns: RegExp[] = [];
  const matches = pac.matchAll(/shExpMatch\(host,\s*"(\*\.[^"]+)"\)/g);

  for (const match of matches) {
    const domain = match[1].replace('*.', '');
    const escaped = domain.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
    patterns.push(new RegExp(`\\.${escaped}$`));
  }

  return patterns;
}
