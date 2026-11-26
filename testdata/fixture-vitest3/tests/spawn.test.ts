import { execSync } from 'node:child_process';
import { describe, expect, it } from 'vitest';

describe('child process uncacheability', () => {
  it('runs node via execSync', () => {
    const out = execSync('node -e "process.stdout.write(\'x\')"', { encoding: 'utf8' });
    expect(out).toBe('x');
  });
});
