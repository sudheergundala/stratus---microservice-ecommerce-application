// Configuration comes only from environment variables, so one image runs in
// every environment. Missing or invalid values stop the process at startup.

export interface Config {
  port: number;
  upstreams: {
    user: string;
    catalog: string;
    order: string;
  };
  upstreamTimeoutMs: number;
  shutdownTimeoutMs: number;
}

function required(name: string): string {
  const value = process.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

function url(name: string): string {
  const value = required(name);
  try {
    new URL(value);
  } catch {
    throw new Error(`${name} must be a valid URL, got "${value}"`);
  }
  return value.replace(/\/+$/, '');
}

function positiveInt(name: string, fallback: number): number {
  const raw = process.env[name];
  if (raw === undefined || raw === '') {
    return fallback;
  }
  const value = Number(raw);
  if (!Number.isInteger(value) || value <= 0) {
    throw new Error(`${name} must be a positive integer, got "${raw}"`);
  }
  return value;
}

export function loadConfig(): Config {
  return {
    port: positiveInt('PORT', 8080),
    upstreams: {
      user: url('USER_SERVICE_URL'),
      catalog: url('CATALOG_SERVICE_URL'),
      order: url('ORDER_SERVICE_URL'),
    },
    upstreamTimeoutMs: positiveInt('UPSTREAM_TIMEOUT_MS', 3000),
    shutdownTimeoutMs: positiveInt('SHUTDOWN_TIMEOUT_MS', 20000),
  };
}