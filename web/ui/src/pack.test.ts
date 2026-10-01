import { expect, test } from 'vitest';
import { parseEvents } from './api';
import { slug, tarGz, validName } from './pack';
import { ago, kept } from './time';

// Reads a tar back the way any tar reader would: 512-byte headers, then data.
async function untar(archive: Blob): Promise<Record<string, string>> {
  const raw = new Uint8Array(
    await new Response(archive.stream().pipeThrough(new DecompressionStream('gzip'))).arrayBuffer(),
  );
  const text = new TextDecoder();
  const field = (at: number, len: number, from: number) =>
    text.decode(raw.subarray(from + at, from + at + len)).replace(/\0.*$/, '');
  const files: Record<string, string> = {};
  for (let at = 0; raw[at] !== 0; ) {
    const size = parseInt(field(124, 12, at), 8);
    const sum = parseInt(field(148, 8, at), 8);
    const header = raw.slice(at, at + 512).fill(0x20, 148, 156);
    expect(header.reduce((a, b) => a + b, 0)).toBe(sum);
    expect(field(257, 5, at)).toBe('ustar');
    const prefix = field(345, 155, at);
    files[(prefix ? `${prefix}/` : '') + field(0, 100, at)] = text.decode(raw.subarray(at + 512, at + 512 + size));
    at += 512 + Math.ceil(size / 512) * 512;
  }
  return files;
}

test('tarGz packs files a tar reader can read back', async () => {
  const deep = `assets/${'very-long-folder-name/'.repeat(5)}chunk.js`;
  expect(deep.length).toBeGreaterThan(100);
  const archive = await tarGz([
    { path: 'index.html', file: new Blob(['<h1>hello</h1>']) },
    { path: 'café/menu.txt', file: new Blob(['x'.repeat(600)]) },
    { path: deep, file: new Blob(['console.log(1)']) },
    { path: 'empty.txt', file: new Blob([]) },
  ]);
  expect(await untar(archive)).toEqual({
    'index.html': '<h1>hello</h1>',
    'café/menu.txt': 'x'.repeat(600),
    [deep]: 'console.log(1)',
    'empty.txt': '',
  });
});

test('names', () => {
  expect(slug('My Garden_Journal')).toBe('my-garden-journal');
  expect(slug('--dist--')).toBe('dist');
  expect(slug('日本')).toBe('');
  expect(validName('my-site')).toBe(true);
  expect(validName('My_Site')).toBe(false);
  expect(validName('-x')).toBe(false);
});

test('parseEvents waits for an event to finish arriving', () => {
  const seen: string[] = [];
  const emit = (event: string, data: string) => seen.push(`${event}=${data}`);
  let rest = parseEvents('event: line\ndata: {"a":1}\n\nevent: li', emit);
  expect(seen).toEqual(['line={"a":1}']);
  rest = parseEvents(`${rest}ne\ndata: {"a":2}\n\nevent: done\ndata: {}\n\n`, emit);
  expect(seen).toEqual(['line={"a":1}', 'line={"a":2}', 'done={}']);
  expect(rest).toBe('');
});

test('ago reads like the pail list', () => {
  const now = new Date('2026-10-01T12:00:00Z');
  const at = (ms: number) => ago(new Date(now.getTime() - ms), now);
  const min = 60_000;
  expect(at(5_000)).toBe('just now');
  expect(at(2 * min)).toBe('2 min ago');
  expect(at(60 * min)).toBe('1 hr ago');
  expect(at(30 * 60 * min)).toBe('yesterday');
  expect(at(3 * 24 * 60 * min)).toBe('3 days ago');
  expect(at(21 * 24 * 60 * min)).toBe('3 weeks ago');
});

test("kept follows the server's deploy limit", () => {
  expect(kept(10)).toBe('the last ten stay');
  expect(kept(3)).toBe('the last three stay');
  expect(kept(25)).toBe('the last 25 stay');
  expect(kept(1)).toBe('the last one stays');
  expect(kept(undefined)).toBe('the latest stay');
});
