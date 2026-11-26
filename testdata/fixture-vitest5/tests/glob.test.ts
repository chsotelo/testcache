import { describe, expect, it } from 'vitest';
import { count } from '../src/registry.js';

describe('import.meta.glob tracking', () => {
  it('counts matched modules', () => {
    expect(count).toBe(1);
  });
});
