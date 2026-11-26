import { describe, expect, it } from 'vitest';
import { add } from '../src/usedByA.js';

describe('suite A', () => {
  it('adds numbers', () => {
    expect(add(2, 3)).toBe(5);
  });
});
