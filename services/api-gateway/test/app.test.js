// Tests run against the compiled output in dist/ (npm test builds first).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import http from 'node:http';
import { SignJWT, exportJWK, generateKeyPair, calculateJwkThumbprint } from 'jose';
import { buildApp } from '../dist/app.js';

// A fake upstream service that records what it receives.
let upstream;
let upstreamUrl;
let lastRequest;

// A fake user-service JWKS endpoint, publishing the test signing key.
let jwksServer;
let jwksUrl;
let signingKey;
let kid;
let jwksRequests = 0;

function listen(server) {
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(`http://127.0.0.1:${server.address().port}`)));
}

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
      res.writeHead(201, { 'content-type': 'application/json', 'cache-control': 'public, max-age=60' });
      res.end(JSON.stringify({ ok: true }));
    });
  });
  upstreamUrl = await listen(upstream);

  const { publicKey, privateKey } = await generateKeyPair('RS256');
  signingKey = privateKey;
  const jwk = await exportJWK(publicKey);
  kid = await calculateJwkThumbprint(jwk);
  const jwks = JSON.stringify({ keys: [{ ...jwk, kid, alg: 'RS256', use: 'sig' }] });
  jwksServer = http.createServer((_req, res) => {
    jwksRequests += 1;
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(jwks);
  });
  jwksUrl = await listen(jwksServer);
});

after(() => {
  upstream.close();
  jwksServer.close();
});

function makeApp(overrides = {}) {
  return buildApp(
    {
      port: 0,
      upstreams: { user: upstreamUrl, catalog: upstreamUrl, order: upstreamUrl },
      upstreamTimeoutMs: 200,
      shutdownTimeoutMs: 1000,
      auth: { jwksUrl, issuer: 'stratus-user-service', audience: 'stratus-api', jwksCacheMaxAgeMs: 600_000 },
      rateLimit: { perMinute: 1000, authPerMinute: 5 },
      trustProxyHops: 0,
      ...overrides,
    },
    'silent',
  );
}

// Signs a token like user-service does; options override single claims.
async function token({ role = 'customer', sub = 'user-1', iss = 'stratus-user-service', aud = 'stratus-api', exp = '15m', key = signingKey, keyId = kid } = {}) {
  return new SignJWT({ role, email: 'sam@example.com' })
    .setProtectedHeader({ alg: 'RS256', kid: keyId })
    .setSubject(sub)
    .setIssuer(iss)
    .setAudience(aud)
    .setIssuedAt()
    .setExpirationTime(exp)
    .sign(key);
}

const bearer = (t) => ({ authorization: `Bearer ${t}` });

// ---------- routing (unchanged behavior) ----------

test('public catalog reads need no token and are forwarded without the /api/v1 prefix', async () => {
  const { server } = makeApp();
  const res = await server.inject({ method: 'GET', url: '/api/v1/products?q=jacket' });
  assert.equal(res.statusCode, 201);
  assert.equal(lastRequest.url, '/products?q=jacket');
  assert.equal(res.headers['cache-control'], 'public, max-age=60', 'cache headers must reach the client');
  assert.ok(lastRequest.headers['x-request-id']);
  assert.equal(lastRequest.headers['x-user-id'], undefined);
});

test('forwards body and allowed headers only', async () => {
  const { server } = makeApp();
  const res = await server.inject({
    method: 'POST',
    url: '/api/v1/orders',
    headers: { ...bearer(await token()), 'content-type': 'application/json', 'idempotency-key': 'k1', cookie: 'secret' },
    payload: '{"sku":"A1"}',
  });
  assert.equal(res.statusCode, 201);
  assert.equal(lastRequest.url, '/orders');
  assert.equal(lastRequest.body, '{"sku":"A1"}');
  assert.equal(lastRequest.headers['idempotency-key'], 'k1');
  assert.equal(lastRequest.headers.cookie, undefined, 'unlisted headers must not be forwarded');
  assert.equal(res.headers['x-request-id'], lastRequest.headers['x-request-id']);
});

test('the old /api/v1/search route is gone', async () => {
  const { server } = makeApp();
  assert.equal((await server.inject({ method: 'GET', url: '/api/v1/search?q=x' })).statusCode, 404);
});

// ---------- authentication ----------

test('orders without a token are rejected with 401 and never reach the service', async () => {
  const { server } = makeApp();
  lastRequest = undefined;
  const res = await server.inject({ method: 'GET', url: '/api/v1/orders/42' });
  assert.equal(res.statusCode, 401);
  assert.match(res.headers['www-authenticate'], /^Bearer /);
  assert.deepEqual(res.json(), { error: 'authentication required' });
  assert.equal(lastRequest, undefined);
});

test('a valid token is accepted and the verified identity is forwarded', async () => {
  const { server } = makeApp();
  const res = await server.inject({ method: 'GET', url: '/api/v1/orders/42', headers: bearer(await token({ sub: 'user-7' })) });
  assert.equal(res.statusCode, 201);
  assert.equal(lastRequest.headers['x-user-id'], 'user-7');
  assert.equal(lastRequest.headers['x-user-role'], 'customer');
});

test('a client cannot claim an identity with its own X-User-* headers', async () => {
  const { server } = makeApp();
  await server.inject({
    method: 'GET',
    url: '/api/v1/orders/42',
    headers: { ...bearer(await token({ sub: 'user-7' })), 'x-user-id': 'admin-1', 'x-user-role': 'admin' },
  });
  assert.equal(lastRequest.headers['x-user-id'], 'user-7');
  assert.equal(lastRequest.headers['x-user-role'], 'customer');
});

test('bad tokens are all rejected with the same 401', async () => {
  const { server } = makeApp();
  const { privateKey: otherKey } = await generateKeyPair('RS256');
  const header = Buffer.from(JSON.stringify({ alg: 'none', typ: 'JWT' })).toString('base64url');
  const claims = Buffer.from(JSON.stringify({ sub: 'x', iss: 'stratus-user-service', aud: 'stratus-api', exp: 9999999999, iat: 1 })).toString('base64url');
  const cases = {
    expired: await token({ exp: Math.floor(Date.now() / 1000) - 120 }),
    'wrong audience': await token({ aud: 'some-other-api' }),
    'wrong issuer': await token({ iss: 'evil' }),
    'signed by another key, our kid': await token({ key: otherKey }),
    'signed by another key, unknown kid': await token({ key: otherKey, keyId: 'unknown' }),
    'alg none': `${header}.${claims}.`,
    garbage: 'not.a.jwt',
  };
  for (const [name, t] of Object.entries(cases)) {
    const res = await server.inject({ method: 'GET', url: '/api/v1/orders/42', headers: bearer(t) });
    assert.equal(res.statusCode, 401, name);
    assert.deepEqual(res.json(), { error: 'authentication required' }, name);
  }
  const res = await server.inject({ method: 'GET', url: '/api/v1/orders/42', headers: { authorization: 'Basic dXNlcjpwYXNz' } });
  assert.equal(res.statusCode, 401, 'non-Bearer schemes are rejected');
});

// ---------- authorization ----------

test('changing the catalog requires the admin role', async () => {
  const { server } = makeApp();
  const put = (headers) => server.inject({ method: 'PUT', url: '/api/v1/products/JKT-001', headers: { ...headers, 'content-type': 'application/json' }, payload: '{}' });
  assert.equal((await put({})).statusCode, 401);
  assert.equal((await put(bearer(await token({ role: 'customer' })))).statusCode, 403);
  const res = await put(bearer(await token({ role: 'admin', sub: 'admin-1' })));
  assert.equal(res.statusCode, 201);
  assert.equal(lastRequest.method, 'PUT');
  assert.equal(lastRequest.headers['x-user-role'], 'admin');
});

// ---------- key fetching ----------

test('public keys are fetched once and cached, not per request', async () => {
  const { server } = makeApp();
  const t = await token();
  const before = jwksRequests;
  for (let i = 0; i < 5; i += 1) {
    assert.equal((await server.inject({ method: 'GET', url: '/api/v1/orders/1', headers: bearer(t) })).statusCode, 201);
  }
  assert.equal(jwksRequests - before, 1);
});

test('keys unreachable and nothing cached: 503, not 401', async () => {
  const { server } = makeApp({ auth: { jwksUrl: 'http://127.0.0.1:1/jwks', issuer: 'stratus-user-service', audience: 'stratus-api', jwksCacheMaxAgeMs: 600_000 } });
  const res = await server.inject({ method: 'GET', url: '/api/v1/orders/1', headers: bearer(await token()) });
  assert.equal(res.statusCode, 503);
  assert.equal(res.headers['retry-after'], '1');
});

test('with keys cached, tokens still verify while user-service is down', async () => {
  const keys = await (await fetch(jwksUrl)).text();
  const temporary = http.createServer((_req, res) => res.end(keys));
  const url = await listen(temporary);
  const { server } = makeApp({ auth: { jwksUrl: url, issuer: 'stratus-user-service', audience: 'stratus-api', jwksCacheMaxAgeMs: 600_000 } });
  const t = await token();
  assert.equal((await server.inject({ method: 'GET', url: '/api/v1/orders/1', headers: bearer(t) })).statusCode, 201);
  await new Promise((resolve) => temporary.close(resolve));
  assert.equal((await server.inject({ method: 'GET', url: '/api/v1/orders/1', headers: bearer(t) })).statusCode, 201);
});

// ---------- rate limiting ----------

test('login is limited per client IP, with Retry-After', async () => {
  const { server } = makeApp();
  const login = () => server.inject({ method: 'POST', url: '/api/v1/auth/login', headers: { 'content-type': 'application/json' }, payload: '{}' });
  for (let i = 0; i < 5; i += 1) {
    assert.equal((await login()).statusCode, 201, `attempt ${i + 1}`);
  }
  const limited = await login();
  assert.equal(limited.statusCode, 429);
  assert.ok(Number(limited.headers['retry-after']) > 0);
  assert.equal(limited.json().error, 'too many requests');
  // Other routes have their own, higher limit.
  assert.equal((await server.inject({ method: 'GET', url: '/api/v1/products' })).statusCode, 201);
});

test('every route has a general limit; probes are never limited', async () => {
  const { server } = makeApp({ rateLimit: { perMinute: 3, authPerMinute: 5 } });
  const codes = [];
  for (let i = 0; i < 4; i += 1) {
    codes.push((await server.inject({ method: 'GET', url: '/api/v1/products' })).statusCode);
  }
  assert.deepEqual(codes, [201, 201, 201, 429]);
  for (let i = 0; i < 10; i += 1) {
    assert.equal((await server.inject('/healthz')).statusCode, 200);
  }
});

test('behind a proxy, limits apply to the real client IP from X-Forwarded-For', async () => {
  const { server } = makeApp({ rateLimit: { perMinute: 2, authPerMinute: 5 }, trustProxyHops: 1 });
  const from = (ip) => server.inject({ method: 'GET', url: '/api/v1/products', headers: { 'x-forwarded-for': ip } });
  assert.equal((await from('203.0.113.1')).statusCode, 201);
  assert.equal((await from('203.0.113.1')).statusCode, 201);
  assert.equal((await from('203.0.113.1')).statusCode, 429);
  assert.equal((await from('203.0.113.2')).statusCode, 201, 'another client is not affected');
});

// ---------- failure handling (unchanged behavior) ----------

test('returns 504 quickly when the upstream hangs', async () => {
  const { server } = makeApp();
  const t = await token();
  const started = Date.now();
  const res = await server.inject({ method: 'GET', url: '/api/v1/orders/slow', headers: bearer(t) });
  assert.equal(res.statusCode, 504);
  assert.ok(Date.now() - started < 450, 'must give up at the configured timeout');
});

test('returns 502 when the upstream is down', async () => {
  const { server } = makeApp({
    upstreams: { user: 'http://127.0.0.1:1', catalog: 'http://127.0.0.1:1', order: 'http://127.0.0.1:1' },
  });
  const res = await server.inject({ method: 'GET', url: '/api/v1/products/1' });
  assert.equal(res.statusCode, 502);
});

test('rejects bodies over 1 MiB with 413', async () => {
  const { server } = makeApp();
  const res = await server.inject({
    method: 'POST',
    url: '/api/v1/auth/register',
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