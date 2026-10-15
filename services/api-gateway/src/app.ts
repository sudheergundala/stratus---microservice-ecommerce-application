import { randomUUID } from 'node:crypto';
import rateLimit from '@fastify/rate-limit';
import Fastify, { type FastifyInstance, type FastifyReply, type FastifyRequest } from 'fastify';
import { createVerifier, type Identity, type Verifier } from './auth.js';
import type { Config } from './config.js';

const BODY_LIMIT_BYTES = 1024 * 1024; // 1 MiB

// Only these request headers are passed to upstream services. Anything else a
// client sends (cookies, X-User-Id, X-Forwarded-*...) is dropped, so a client
// can never claim an identity: X-User-* headers are set only by the gateway.
const FORWARDED_HEADERS = ['authorization', 'content-type', 'idempotency-key'];

// Who may call a route: anyone, any signed-in user, or admins only.
type Access = 'public' | 'user' | 'admin';

export interface App {
  server: FastifyInstance;
  startDraining: () => void;
}

// buildApp creates the gateway without starting it, so tests can drive it
// with server.inject() and no network port.
export function buildApp(config: Config, logLevel = 'info', verify: Verifier = createVerifier(config.auth)): App {
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
    // Behind a load balancer the TCP peer is the load balancer; the client IP
    // is in X-Forwarded-For. Trust exactly as many hops as there are proxies.
    // (Trusting every hop instead would let any client fake its IP.)
    trustProxy: (_address: string, hop: number) => hop < config.trustProxyHops,
  });

  // Keep JSON bodies as raw bytes and forward them untouched.
  server.addContentTypeParser('application/json', { parseAs: 'buffer' }, (_req, body, done) => {
    done(null, body);
  });

  server.addHook('onSend', async (req, reply) => {
    reply.header('x-request-id', req.id);
  });

  // Per client IP. In-memory: each gateway replica counts separately (see README).
  server.register(rateLimit, {
    global: true,
    max: config.rateLimit.perMinute,
    timeWindow: 60_000,
    errorResponseBuilder: (_req, context) => ({
      statusCode: 429,
      error: 'too many requests',
      retry_after_seconds: Math.ceil(context.ttl / 1000),
    }),
  });

  // Routes are registered after the rate limiter so it applies to them.
  server.register(async (app) => {
    // Liveness: the process can respond. Never checks upstream services.
    app.get('/healthz', { config: { rateLimit: false } }, async () => ({ status: 'ok' }));

    // Readiness: fails while shutting down so load balancers stop sending traffic.
    app.get('/readyz', { config: { rateLimit: false } }, async (_req, reply) => {
      if (draining) {
        return reply.code(503).send({ status: 'draining' });
      }
      return { status: 'ready' };
    });

    // Password guessing protection: login and register get a much lower limit.
    const authLimit = { rateLimit: { max: config.rateLimit.authPerMinute, timeWindow: 60_000 } };
    for (const path of ['/api/v1/auth/login', '/api/v1/auth/register']) {
      app.post(path, { config: authLimit }, (req, reply) =>
        handle(req, reply, config.upstreams.user, () => 'public'),
      );
    }

    // Public route prefix -> upstream service and access rule. The /api/v1
    // prefix is removed before forwarding: /api/v1/orders/42 -> {order}/orders/42
    const routes: Array<[string, string, (method: string) => Access]> = [
      ['/api/v1/auth', config.upstreams.user, () => 'public'],
      // Browsing the catalog is public; changing it is for admins only.
      ['/api/v1/products', config.upstreams.catalog, (m) => (m === 'GET' || m === 'HEAD' ? 'public' : 'admin')],
      ['/api/v1/orders', config.upstreams.order, () => 'user'],
    ];

    for (const [prefix, upstream, access] of routes) {
      const handler = (req: FastifyRequest, reply: FastifyReply) => handle(req, reply, upstream, access);
      app.all(prefix, handler);
      app.all(`${prefix}/*`, handler);
    }
  });

  server.setNotFoundHandler((_req, reply) => {
    reply.code(404).send({ error: 'route not found' });
  });

  async function handle(req: FastifyRequest, reply: FastifyReply, upstream: string, access: (method: string) => Access) {
    const needed = access(req.method);
    let identity: Identity | undefined;

    if (needed !== 'public') {
      const result = await verify(req.headers.authorization);
      if (!result.ok && result.status === 503) {
        req.log.error({ reason: result.reason }, 'cannot verify token: signing keys unavailable');
        return reply.code(503).header('retry-after', '1').send({ error: 'authentication temporarily unavailable' });
      }
      if (!result.ok) {
        // The reason (e.g. ERR_JWT_EXPIRED) is logged, never returned: the
        // client gets the same answer whatever was wrong with the token.
        req.log.info({ reason: result.reason }, 'request rejected: not authenticated');
        return reply
          .code(401)
          .header('www-authenticate', 'Bearer realm="stratus", error="invalid_token"')
          .send({ error: 'authentication required' });
      }
      identity = result.identity;
      if (needed === 'admin' && identity.role !== 'admin') {
        req.log.info({ userId: identity.userId, role: identity.role }, 'request rejected: admin role required');
        return reply.code(403).send({ error: 'forbidden' });
      }
    }

    return forward(req, reply, upstream, config.upstreamTimeoutMs, identity);
  }

  return {
    server,
    startDraining: () => {
      draining = true;
    },
  };
}

async function forward(
  req: FastifyRequest,
  reply: FastifyReply,
  upstream: string,
  timeoutMs: number,
  identity: Identity | undefined,
) {
  const target = upstream + req.url.replace(/^\/api\/v1/, '');

  const headers: Record<string, string> = { 'x-request-id': String(req.id) };
  for (const name of FORWARDED_HEADERS) {
    const value = req.headers[name];
    if (typeof value === 'string') {
      headers[name] = value;
    }
  }
  // The verified identity, so services don't each have to parse the token.
  if (identity) {
    headers['x-user-id'] = identity.userId;
    headers['x-user-role'] = identity.role;
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
    for (const name of ['content-type', 'cache-control', 'retry-after']) {
      const value = res.headers.get(name);
      if (value) {
        reply.header(name, value);
      }
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