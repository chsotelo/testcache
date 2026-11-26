import { describe, expect, it } from 'vitest';

describe('env dependency tracking', () => {
  it('reads dynamic process.env key', () => {
    const val = process.env.TC_TEST_DYNAMIC_VAR ?? 'default-val';
    expect(val).toBe('default-val');
  });
});
