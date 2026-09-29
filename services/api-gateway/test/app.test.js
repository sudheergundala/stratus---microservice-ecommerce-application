// Tests run against the compiled output in dist/ (npm test builds first).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { buildApp } from '../dist/app.js';

// A fake upstream service that records what it receives.
let upstream;
let upstreamUrl;
let lastRequest;

before(async () => {
  upstream = http.createServer((req, res) => {
    let body = '';
    req.on('data', (chunk) => (body += chunk));
    req.on('end', () => {
      lastRequest = { method: req.method, url: req.url, headers: req.headers, body };
      if (req.url === '/orders/slow') {
        setTimeout(() => res.end('{}'), 500);
        return;
      }
      res.writeHead(201, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ ok: true }));
    });
  });
  await new Promise((resolve) => upstream.listen(0, '127.0.0.1', resolve));
  upstreamUrl = `http://127.0.0.1:${upstream.address().port}`;
});

after(() => upstream.close());

function makeApp(overrides = {}) {
  return buildApp(
    {
      port: 0,
      upstreams: { user: upstreamUrl, catalog: upstreamUrl, order: upstreamUrl },
      upstreamTimeoutMs: 200,
      shutdownTimeoutMs: 1000,
      ...overrides,
    },
    'silent',
  );
}

test('forwards to the upstream without the /api/v1 prefix, with body and headers', async () => {
  const { server } = makeApp();
  const res = await server.inject({
    method: 'POST',
    url: '/api/v1/orders',
    headers: { 'content-type': 'application/json', 'idempotency-key': 'k1', cookie: 'secret' },
    payload: '{"sku":"A1"}',
  });
  assert.equal(res.statusCode, 201);
  assert.equal(lastRequest.url, '/orders');
  assert.equal(lastRequest.body, '{"sku":"A1"}');
  assert.equal(lastRequest.headers['idempotency-key'], 'k1');
  assert.equal(lastRequest.headers.cookie, undefined, 'unlisted headers must not be forwarded');
  assert.ok(lastRequest.headers['x-request-id'], 'request id must be forwarded');
  assert.equal(res.headers['x-request-id'], lastRequest.headers['x-request-id']);
});

test('returns 504 quickly when the upstream hangs', async () => {
  const { server } = makeApp();
  const started = Date.now();
  const res = await server.inject({ method: 'GET', url: '/api/v1/orders/slow' });
  assert.equal(res.statusCode, 504);
  assert.ok(Date.now() - started < 450, 'must give up at the configured timeout');
});

test('returns 502 when the upstream is down', async () => {
  const { server } = makeApp({
    upstreams: { user: 'http://127.0.0.1:1', catalog: 'http://127.0.0.1:1', order: 'http://127.0.0.1:1' },
  });
  const res = await server.inject({ method: 'GET', url: '/api/v1/orders/1' });
  assert.equal(res.statusCode, 502);
});

test('rejects bodies over 1 MiB with 413', async () => {
  const { server } = makeApp();
  const res = await server.inject({
    method: 'POST',
    url: '/api/v1/orders',
    headers: { 'content-type': 'application/json' },
    payload: 'x'.repeat(1024 * 1024 + 1),
  });
  assert.equal(res.statusCode, 413);
});

test('unknown routes return 404 JSON', async () => {
  const { server } = makeApp();
  const res = await server.inject({ method: 'GET', url: '/admin' });
  assert.equal(res.statusCode, 404);
  assert.deepEqual(res.json(), { error: 'route not found' });
});

test('healthz stays 200 while draining; readyz turns 503', async () => {
  const { server, startDraining } = makeApp();
  assert.equal((await server.inject('/healthz')).statusCode, 200);
  assert.equal((await server.inject('/readyz')).statusCode, 200);
  startDraining();
  assert.equal((await server.inject('/readyz')).statusCode, 503);
  assert.equal((await server.inject('/healthz')).statusCode, 200);
});