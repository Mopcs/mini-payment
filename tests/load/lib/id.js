// Identifier helper for idempotency keys.
//
// Each transfer and deposit in the suite needs its own key. Reusing one across
// independent operations would make them replays of each other and quietly turn
// a throughput test into an idempotency test.

import { randomBytes } from 'k6/crypto';

export function uuid() {
  const bytes = new Uint8Array(randomBytes(16));
  // RFC 4122 version 4 and variant bits, so the value is a real UUID and not
  // just 16 random bytes that happen to be formatted like one.
  bytes[6] = (bytes[6] & 0x0f) | 0x40;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;

  const hex = [];
  for (let i = 0; i < 16; i++) {
    hex.push((bytes[i] + 0x100).toString(16).slice(1));
  }
  return (
    hex.slice(0, 4).join('') +
    '-' +
    hex.slice(4, 6).join('') +
    '-' +
    hex.slice(6, 8).join('') +
    '-' +
    hex.slice(8, 10).join('') +
    '-' +
    hex.slice(10, 16).join('')
  );
}
