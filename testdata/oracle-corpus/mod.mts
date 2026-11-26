export type Mode = 'fast' | 'safe';

export function pickMode(fast: boolean): Mode {
  return fast ? 'fast' : 'safe';
}
