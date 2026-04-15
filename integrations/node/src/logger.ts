export type LogLevel = 'error' | 'info' | 'debug';

const levels: Record<LogLevel, number> = {
  error: 0,
  info: 1,
  debug: 2,
};

export interface Logger {
  error: (...args: unknown[]) => void;
  info: (...args: unknown[]) => void;
  debug: (...args: unknown[]) => void;
}

const validLevels = new Set<string>(Object.keys(levels));

export function isLogLevel(value: string): value is LogLevel {
  return validLevels.has(value);
}

export function createLogger(level: LogLevel): Logger {
  const threshold = levels[level];

  const noop = () => {};

  return {
    error: console.error.bind(console, '[dev-proxy]'),
    info: threshold >= levels.info ? console.log.bind(console, '[dev-proxy]') : noop,
    debug: threshold >= levels.debug ? console.log.bind(console, '[dev-proxy]') : noop,
  };
}
