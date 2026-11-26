import http from 'node:http';
import { describe, expect, it } from 'vitest';

describe('network access uncacheability', () => {
  it('fetches from local http server', async () => {
    const server = http.createServer((_req, res) => {
      res.end('ok');
    });
    await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
    try {
      const addr = server.address();
      if (!addr || typeof addr === 'string') {
        throw new Error('expected tcp address');
      }
      const res = await fetch(`http://127.0.0.1:${addr.port}/ping`);
      expect(await res.text()).toBe('ok');
    } finally {
      await new Promise<void>((resolve, reject) => server.close((err) => (err ? reject(err) : resolve())));
    }
  });
});
