import { readFileSync } from 'node:fs';
import { cp, mkdir, readdir, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { pailRoutes } from './routes.js';

const NAME = 'astro-pail';

// Packages that can't be built into the function's own files: they come with
// programs compiled for one kind of machine. Pail installs them where the
// function runs instead.
const NATIVE = ['sharp'];

/**
 * Deploys an Astro site to Pail. Prerendered pages and assets are served as
 * files; pages rendered on demand, and API routes, are answered by a function.
 * @param {import('./index.js').Options} [options]
 * @returns {import('astro').AstroIntegration}
 */
export default function pail(options = {}) {
  const fn = options.name ?? 'ssr';
  if (!/^[a-z0-9]([a-z0-9-]{0,30}[a-z0-9])?$/.test(fn)) {
    throw new Error(`${NAME}: function names are lowercase letters, numbers and dashes, like ssr. Got "${fn}".`);
  }
  const external = [...new Set([...NATIVE, ...(options.external ?? [])])];

  /** @type {import('astro').AstroConfig} */
  let config;
  /** @type {import('astro').IntegrationResolvedRoute[]} */
  let routes = [];
  let buildOutput = 'static';

  return {
    name: NAME,
    hooks: {
      'astro:config:setup': ({ updateConfig }) => {
        updateConfig({
          // Redirects are answered by the function, not by pages that refresh.
          build: { redirects: false },
          vite: { plugins: [bundle(external)] },
        });
      },
      'astro:config:done': ({ config: done, setAdapter, buildOutput: output }) => {
        config = done;
        buildOutput = output;
        setAdapter({
          name: NAME,
          entrypointResolution: 'auto',
          serverEntrypoint: new URL('./server.js', import.meta.url),
          // Astro looks this one up as a path, not an address.
          previewEntrypoint: fileURLToPath(new URL('./preview.js', import.meta.url)),
          // Astro has worked out by now whether any route is rendered on
          // demand. A site with none is built as it would be with no adapter.
          adapterFeatures: { middlewareMode: 'classic', buildOutput: output },
          supportedAstroFeatures: {
            staticOutput: 'stable',
            hybridOutput: 'stable',
            serverOutput: 'stable',
            envGetSecret: 'stable',
            sharpImageService: {
              support: 'limited',
              message:
                'Images in prerendered pages are optimized by the build. In pages rendered on demand they are optimized by the function on each request, which has to fetch the original from the pail’s own address first.',
              suppress: 'default',
            },
            i18nDomains: 'unsupported',
          },
        });
      },
      'astro:routes:resolved': ({ routes: resolved }) => {
        routes = resolved;
      },
      'astro:build:done': async ({ logger }) => {
        if (buildOutput !== 'server') {
          logger.info(
            'Nothing is rendered on demand, so there is no function to make. Pail serves the build as files.',
          );
          return;
        }
        const outDir = fileURLToPath(config.outDir);
        const clientDir = fileURLToPath(config.build.client);
        const serverDir = fileURLToPath(config.build.server);

        await underBase(clientDir, config.base);
        const files = await listFiles(clientDir);
        await keepErrorPages(clientDir, config.base, serverDir);
        const deps = await writeFunctionPackage(serverDir, external, fileURLToPath(config.root));

        const onDemand = routes.filter((route) => !route.isPrerendered);
        // A route's segments leave the base out; the paths Pail sees have it.
        const under = segments(config.base).map((content) => [{ content, dynamic: false, spread: false }]);
        const pailJSON = {
          ...(files.length > 0 && { static: relative(outDir, clientDir) }),
          functions: {
            [fn]: {
              src: relative(outDir, serverDir),
              lang: 'node',
              cmd: ['node', config.build.serverEntry],
              memory: options.memory ?? '256MB',
              ...pick(options, ['timeout', 'idle', 'max']),
              // Every request starts Node afresh. What it compiled is kept
              // where the next request to the same copy finds it.
              env: { NODE_COMPILE_CACHE: '/tmp/node-compile-cache', ...options.env },
            },
          },
          routes: pailRoutes(
            files,
            onDemand.map((route) => [...under, ...route.segments]),
            {
              fn,
              // A page of the site's own for paths that match nothing has to
              // come from the function, so no folder is handed to files whole.
              // The 404 page Astro supplies when the site has none isn't worth that.
              wholeFolders: !onDemand.some((route) => route.pattern === '/404' && route.origin !== 'internal'),
              always: [[...segments(config.base), ...segments(config.build.assets)].join('/')],
            },
          ),
        };
        await writeFile(path.join(outDir, 'pail.json'), `${JSON.stringify(pailJSON, null, 2)}\n`);

        const pages = onDemand.filter((route) => route.type === 'page' || route.type === 'endpoint').length;
        logger.info(
          `Wrote pail.json: ${files.length} ${plural(files.length, 'file')}, and ${fn} for ${pages} ${plural(pages, 'route')} rendered on demand${deps.length > 0 ? ` (Pail installs ${deps.join(', ')} for it)` : ''}.`,
        );
      },
    },
  };
}

/**
 * Builds the site's packages into the function's own files, so that it runs
 * with nothing installed beside it. Only the function is built this way:
 * prerendering runs here, where the packages already are.
 * @param {string[]} external
 * @returns {import('vite').Plugin}
 */
function bundle(external) {
  return {
    name: `${NAME}:bundle`,
    apply: 'build',
    configEnvironment(name) {
      if (name === 'ssr') return { resolve: { noExternal: true, external } };
    },
  };
}

/**
 * Says which packages Pail has to install for the function: the ones left
 * out of its files that it does ask for, at the versions installed here.
 */
async function writeFunctionPackage(serverDir, external, root) {
  const code = [];
  for (const file of await listFiles(serverDir)) {
    if (/\.[cm]?js$/.test(file)) code.push(await readFile(path.join(serverDir, file), 'utf8'));
  }
  const dependencies = {};
  for (const name of external) {
    const quoted = new RegExp(`["'\`]${name.replace(/[.*+?^${}()|[\]\\/]/g, '\\$&')}(/[^"'\`]*)?["'\`]`);
    if (code.some((source) => quoted.test(source))) dependencies[name] = installedVersion(name, root);
  }
  const names = Object.keys(dependencies);
  if (names.length > 0) {
    const pkg = { name: 'pail-function', private: true, type: 'module', dependencies };
    await writeFile(path.join(serverDir, 'package.json'), `${JSON.stringify(pkg, null, 2)}\n`);
  }
  return names;
}

/**
 * The version of a package the build ran with: the project's own, or the
 * one Astro brought, which is where a package manager that doesn't hoist
 * keeps sharp.
 */
function installedVersion(name, root) {
  const project = createRequire(path.join(root, 'package.json'));
  const from = [
    () => project,
    () => createRequire(project.resolve('astro/package.json')),
    () => createRequire(import.meta.url),
  ];
  for (const requirer of from) {
    try {
      let dir = path.dirname(requirer().resolve(name));
      while (!dir.endsWith(path.join('node_modules', name))) {
        if (dir === path.dirname(dir)) throw new Error('not found');
        dir = path.dirname(dir);
      }
      return JSON.parse(readFileSync(path.join(dir, 'package.json'), 'utf8')).version;
    } catch {}
  }
  return '*';
}

/**
 * Astro leaves a site with a base as if it were at the top. Pail serves a
 * file at its own path, so the files move under the base.
 */
async function underBase(clientDir, base) {
  const under = segments(base);
  if (under.length === 0) return;
  const moved = `${clientDir.replace(/[\\/]+$/, '')}.pail-base`;
  await rm(moved, { recursive: true, force: true });
  await rename(clientDir, moved);
  await mkdir(path.join(clientDir, ...under.slice(0, -1)), { recursive: true });
  await rename(moved, path.join(clientDir, ...under));
}

/** Keeps the prerendered 404 and 500 pages with the function, which serves them itself. */
async function keepErrorPages(clientDir, base, serverDir) {
  for (const status of ['404', '500']) {
    for (const page of [`${status}.html`, path.join(status, 'index.html')]) {
      try {
        await cp(path.join(clientDir, ...segments(base), page), path.join(serverDir, '_pail', `${status}.html`));
        break;
      } catch {}
    }
  }
}

/** Every file under a folder, as paths inside it with forward slashes. */
async function listFiles(dir) {
  try {
    const entries = await readdir(dir, { recursive: true, withFileTypes: true });
    return entries
      .filter((entry) => entry.isFile())
      .map((entry) => path.relative(dir, path.join(entry.parentPath, entry.name)).split(path.sep).join('/'))
      .sort();
  } catch (err) {
    if (err.code === 'ENOENT') return [];
    throw err;
  }
}

const segments = (p) => p.split('/').filter(Boolean);
const relative = (from, to) => `./${path.relative(from, to).split(path.sep).join('/')}`;
const plural = (n, word) => (n === 1 ? word : `${word}s`);
const pick = (from, keys) =>
  Object.fromEntries(keys.filter((key) => from[key] !== undefined).map((key) => [key, from[key]]));
