import assert from 'node:assert/strict';
import { test } from 'node:test';
import { pailRoutes, reaches } from '../src/routes.js';

/** A route's segments, from its pattern: /blog/[slug] or /docs/[...rest]. */
const route = (pattern) =>
  pattern
    .split('/')
    .filter(Boolean)
    .map((segment) => {
      const dynamic = segment.startsWith('[');
      return [{ content: segment.replace(/^\[|\]$/g, ''), dynamic, spread: segment.startsWith('[...') }];
    });

const paths = (routes) => routes.map((r) => `${r.path} → ${r.to}`);

test('files answer their own paths, and the function the rest', () => {
  const files = ['index.html', 'about/index.html', 'pricing.html', 'robots.txt', '_astro/a.css', '_astro/b.js'];
  assert.deepEqual(paths(pailRoutes(files, [route('/api/notes'), route('/_image')], { fn: 'ssr' })), [
    '/ → static',
    '/pricing → static',
    '/robots.txt → static',
    '/_astro/* → static',
    '/about/* → static',
    '/* → function:ssr',
  ]);
});

test('a folder an on-demand route reaches is listed file by file', () => {
  const files = [
    'blog/index.html',
    'blog/first/index.html',
    'blog/feed.xml',
    'blog/img/a.png',
    'blog/img/big/b.png',
    'docs/a/index.html',
  ];
  assert.deepEqual(paths(pailRoutes(files, [route('/blog/[slug]')], { fn: 'ssr' })), [
    '/blog/feed.xml → static',
    '/blog → static',
    '/blog/first → static',
    // /blog/img could be a post called img, so the folder isn't handed over whole…
    '/blog/img/a.png → static',
    // …but [slug] is one segment, and reaches nothing two folders down.
    '/blog/img/big/* → static',
    '/docs/* → static',
    '/* → function:ssr',
  ]);
});

test('a catch-all leaves no folder whole but the build’s assets', () => {
  const files = ['_astro/a.css', 'blog/first/index.html', 'blog/img/a.png'];
  const routes = pailRoutes(files, [route('/[...path]')], { fn: 'ssr', always: ['_astro'] });
  assert.deepEqual(paths(routes), [
    '/_astro/* → static',
    '/blog/first → static',
    '/blog/img/a.png → static',
    '/* → function:ssr',
  ]);
});

test('with a 404 page rendered on demand, no folder goes to files whole', () => {
  const files = ['_astro/a.css', 'docs/a/index.html', 'docs/b.html'];
  const routes = pailRoutes(files, [route('/404')], { fn: 'web', wholeFolders: false, always: ['_astro'] });
  assert.deepEqual(paths(routes), ['/_astro/* → static', '/docs/b → static', '/docs/a → static', '/* → function:web']);
});

test('with no files, the function answers everything', () => {
  assert.deepEqual(paths(pailRoutes([], [route('/api/[...path]')], { fn: 'ssr' })), ['/* → function:ssr']);
});

test('a file Pail has no route for is left to the function', () => {
  assert.deepEqual(paths(pailRoutes(['a*b.txt', 'c.txt'], [], { fn: 'ssr' })), [
    '/c.txt → static',
    '/* → function:ssr',
  ]);
});

test('reaches says whether a route could answer under a folder', () => {
  assert.equal(reaches(route('/blog/[slug]'), ['blog']), true);
  assert.equal(reaches(route('/blog'), ['blog']), true);
  assert.equal(reaches(route('/Blog'), ['blog']), true);
  assert.equal(reaches(route('/blog'), ['blog', 'img']), false);
  assert.equal(reaches(route('/blog/[slug]'), ['blog', 'img']), true);
  assert.equal(reaches(route('/blog/[slug]'), ['blog', 'img', 'big']), false);
  assert.equal(reaches(route('/[lang]/about'), ['en']), true);
  assert.equal(reaches(route('/[lang]/about'), ['en', 'img']), false);
  assert.equal(reaches(route('/docs/[...rest]'), ['docs', 'a', 'b']), true);
  assert.equal(reaches(route('/docs/[...rest]'), ['blog']), false);
  assert.equal(reaches(route('/'), ['blog']), false);
  // A segment made of more than one part: /[lang]-about.
  const mixed = [
    [
      { content: 'lang', dynamic: true, spread: false },
      { content: '-about', dynamic: false, spread: false },
    ],
  ];
  assert.equal(reaches(mixed, ['en-about']), true);
});
