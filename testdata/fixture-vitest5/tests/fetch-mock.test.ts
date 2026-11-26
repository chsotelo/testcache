import { afterEach, describe, expect, it, vi } from 'vitest';

describe('mocked fetch stays cacheable', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('uses vi.fn to mock globalThis.fetch', async () => {
    const mockFetch = vi.fn(async () => new Response('mocked-ok', { status: 200 }));
    vi.stubGlobal('fetch', mockFetch);
    const res = await fetch('https://example.invalid/api');
    expect(await res.text()).toBe('mocked-ok');
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });
});
