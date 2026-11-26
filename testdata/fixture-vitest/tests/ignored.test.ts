import { describe, expect, it } from 'vitest';

describe('excluded test file', () => {
  it('should never be discovered or executed by vitest', () => {
    expect(true).toBe(false);
  });
});
