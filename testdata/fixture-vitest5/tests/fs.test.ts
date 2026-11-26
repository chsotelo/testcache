import { readFileSync } from 'node:fs';
import fs from 'fs';
import { readdir } from 'node:fs/promises';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

const rootDir = path.resolve(import.meta.dirname, '..');

describe('fs dependency tracking', () => {
  it('reads theme.css with named readFileSync', () => {
    const css = readFileSync(path.join(rootDir, 'data', 'theme.css'), 'utf8');
    expect(css).toContain('--accent: blue');
  });

  it('checks optional file with default fs.existsSync', () => {
    const exists = fs.existsSync(path.join(rootDir, 'data', 'optional-flag.txt'));
    expect(exists).toBe(false);
  });

  it('lists data/dir with fs/promises readdir', async () => {
    const entries = await readdir(path.join(rootDir, 'data', 'dir'));
    expect(entries.sort()).toEqual(['item1.txt']);
  });
});
