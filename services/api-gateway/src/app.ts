import { randomUUID } from 'node:crypto';
import Fastify, { type FastifyInstance, type FastifyReply, type FastifyRequest } from 'fastify';
import type { Config } from './config.js';

const BODY_LIMIT_BYTES = 1024 * 1024; // 1 MiB

// Only these request headers are passed to upstream services.
const FORWARDED_HEADERS = ['authorization', 'content-type', 'idempotency-key'];

export interface App {
  server: FastifyInstance;
  startDraining: () => void;
}

// buildApp creates the gateway without starting it, so tests can drive it
// with server.inject() and no network port.
export function buildApp(config: Config, logLevel = 'info'): App {
  let draining = false;

  const server = Fastify({
    logger: {
      level: logLevel,
      base: { service: 'api-gateway' },
      redact: ['req.headers.authorization'],
    },
    bodyLimit: BODY_LIMIT_BYTES,
    requestIdHeader: 'x-request-id',
    genReqId: () => randomUUID(),
  });

  // Keep JSON bodies as raw bytes and forward them untouched.
  server.addContentTypeParser('application/json', { parseAs: 'buffer' }, (_req, body, done) => {
    done(null, body);
  });

  server.addHook('onSend', async (req, reply) => {
    reply.header('x-request-id', req.id);
  });

  // Liveness: the process can respond. Never checks upstream services.
  server.get('/healthz', async () => ({ status: 'ok' }));

  // Readiness: fails while shutting down so load balancers stop sending traffic.
  server.get('/readyz', async (_req, reply) => {
    if (draining) {
      return reply.code(503).send({ status: 'draining' });
    }
    return { status: 'ready' };
  });

  // Public route prefix -> upstream service. The /api/v1 prefix is removed
  // before forwarding: /api/v1/orders/42 -> {order}/orders/42
  const routes: Array<[string, string]> = [
    ['/api/v1/auth', config.upstreams.user],
    ['/api/v1/products', config.upstreams.catalog],
    ['/api/v1/search', config.upstreams.catalog],
    ['/api/v1/orders', config.upstreams.order],
  ];

  for (const [prefix, upstream] of routes) {
    const handler = (req: FastifyRequest, reply: FastifyReply) =>
      forward(req, reply, upstream, config.upstreamTimeoutMs);
    server.all(prefix, handler);
    server.all(`${prefix}/*`, handler);
  }

  server.setNotFoundHandler((_req, reply) => {
    reply.code(404).send({ error: 'route not found' });
  });

  return {
    server,
    startDraining: () => {
      draining = true;
    },
  };
}

async function forward(req: FastifyRequest, reply: FastifyReply, upstream: string, timeoutMs: number) {
  const target = upstream + req.url.replace(/^\/api\/v1/, '');

  const headers: Record<string, string> = { 'x-request-id': String(req.id) };
  for (const name of FORWARDED_HEADERS) {
    const value = req.headers[name];
    if (typeof value === 'string') {
      headers[name] = value;
    }
  }

  const hasBody = req.method !== 'GET' && req.method !== 'HEAD' && Buffer.isBuffer(req.body);

  try {
    const res = await fetch(target, {
      method: req.method,
      headers,
      body: hasBody ? (req.body as Buffer).toString('utf8') : undefined, // JSON is UTF-8 text
      // Never wait forever on a dependency: a hung upstream must not hang the gateway.
      signal: AbortSignal.timeout(timeoutMs),
    });
    const body = Buffer.from(await res.arrayBuffer());
    reply.code(res.status);
    const contentType = res.headers.get('content-type');
    if (contentType) {
      reply.header('content-type', contentType);
    }
    return reply.send(body);
  } catch (err) {
    const timedOut = err instanceof Error && err.name === 'TimeoutError';
    req.log.error({ err, upstream, timedOut }, 'upstream request failed');
    return reply
      .code(timedOut ? 504 : 502)
      .send({ error: timedOut ? 'upstream timed out' : 'upstream unavailable' });
  }
}