import { describe, expect, it } from 'vitest';

describe('snapshot dependency tracking', () => {
  it('matches object snapshot', () => {
    expect({ a: 1 }).toMatchSnapshot();
  });

  it('matches file snapshot', async () => {
    await expect('hello file snapshot\n').toMatchFileSnapshot('./snapshots/file-snap.txt');
  });
});
