import { describe, expect, it } from 'vitest';
import { multiply } from '../src/usedByB.js';

describe('suite B', () => {
  it('multiplies numbers', () => {
    expect(multiply(3, 4)).toBe(12);
  });
});
