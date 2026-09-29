import { buildApp } from './app.js';
import { loadConfig, type Config } from './config.js';

let config: Config;
try {
  config = loadConfig();
} catch (err) {
  // Same JSON shape as every other log line, then exit 2 = bad configuration.
  console.log(
    JSON.stringify({
      level: 'error',
      service: 'api-gateway',
      msg: 'invalid configuration',
      error: (err as Error).message,
    }),
  );
  process.exit(2);
}

const { server, startDraining } = buildApp(config);

// Node.js does NOT exit gracefully on SIGTERM by default. Without these
// handlers, `docker stop` or a Kubernetes rollout would wait the full grace
// period and then SIGKILL the process mid-request.
let shuttingDown = false;
async function shutdown(signal: string) {
  if (shuttingDown) return;
  shuttingDown = true;
  server.log.info({ signal }, 'shutdown started');
  startDraining();

  const timer = setTimeout(() => {
    server.log.error('graceful shutdown timed out');
    process.exit(1);
  }, config.shutdownTimeoutMs);
  timer.unref();

  await server.close(); // stop accepting, wait for in-flight requests
  server.log.info('shutdown complete');
  process.exit(0);
}
process.on('SIGTERM', () => void shutdown('SIGTERM'));
process.on('SIGINT', () => void shutdown('SIGINT'));

// A bug that escapes every handler leaves the process in an unknown state:
// log it and exit so the orchestrator starts a clean replacement.
process.on('uncaughtException', (err) => {
  server.log.fatal({ err }, 'uncaught exception');
  process.exit(1);
});
process.on('unhandledRejection', (reason) => {
  server.log.fatal({ reason }, 'unhandled promise rejection');
  process.exit(1);
});

try {
  // 0.0.0.0, not localhost: inside a container, localhost is unreachable from outside.
  await server.listen({ host: '0.0.0.0', port: config.port });
} catch (err) {
  server.log.fatal({ err }, 'failed to start');
  process.exit(1);
}