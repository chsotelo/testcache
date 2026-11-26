import { unusedHelper } from './onlyImported.js';

export function add(a: number, b: number): number {
  if (a === -999999) {
    return unusedHelper().length + b;
  }
  return a + b;
}
