// Verifies access tokens issued by user-service, without calling it per
// request: the gateway fetches user-service's public keys (JWKS) once, caches
// them, and checks each token's signature and claims locally.
import { createRemoteJWKSet, errors, jwtVerify } from 'jose';
import type { Config } from './config.js';

export interface Identity {
  userId: string;
  role: string;
}

export type AuthResult =
  | { ok: true; identity: Identity }
  // 401: no token, or a token we must not accept (expired, forged, wrong audience...).
  | { ok: false; status: 401; reason: string }
  // 503: the token can't be checked right now (keys unreachable). Not the client's fault.
  | { ok: false; status: 503; reason: string };

// jose error codes that mean "couldn't get the keys", not "bad token".
const KEY_SOURCE_FAILURES = new Set(['ERR_JOSE_GENERIC', 'ERR_JWKS_TIMEOUT', 'ERR_JWKS_INVALID']);

export type Verifier = (authorization: string | undefined) => Promise<AuthResult>;

export function createVerifier(auth: Config['auth']): Verifier {
  const jwks = createRemoteJWKSet(new URL(auth.jwksUrl), {
    timeoutDuration: 2000,
    // An unknown key id (after a key rotation) triggers a refetch, at most
    // once per 30 s, so forged kids can't make us hammer user-service.
    cooldownDuration: 30_000,
    cacheMaxAge: auth.jwksCacheMaxAgeMs,
  });

  return async (authorization) => {
    const match = /^Bearer ([A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)$/.exec(authorization ?? '');
    if (!match) {
      return { ok: false, status: 401, reason: authorization ? 'malformed authorization header' : 'missing token' };
    }
    try {
      const { payload } = await jwtVerify(match[1], jwks, {
        issuer: auth.issuer,
        audience: auth.audience,
        // Only the algorithm user-service uses. Blocks "alg: none" and
        // algorithm-confusion attacks.
        algorithms: ['RS256'],
        // Small allowance for clock differences between machines.
        clockTolerance: 30,
        requiredClaims: ['sub', 'exp', 'iat'],
      });
      const role = typeof payload.role === 'string' ? payload.role : 'customer';
      return { ok: true, identity: { userId: String(payload.sub), role } };
    } catch (err) {
      if (err instanceof errors.JOSEError && !KEY_SOURCE_FAILURES.has(err.code)) {
        return { ok: false, status: 401, reason: err.code };
      }
      // The keys couldn't be fetched: network error, timeout, non-200 or bad
      // JSON from user-service. The token may be fine; we just can't check it.
      return { ok: false, status: 503, reason: err instanceof Error ? err.message : 'jwks unavailable' };
    }
  };
}