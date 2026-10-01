// pail.json's routes say what answers each path, and the first that fits
// wins. A route's path covers itself, or with a star on the end, everything
// under it. So the files the build left get routes of their own, and
// whatever is left over goes to the function.

/**
 * @typedef {{ content: string, dynamic: boolean, spread: boolean }} RoutePart
 * @typedef {{ path: string, to: string }} Route
 */

/**
 * The routes for a build.
 * @param {string[]} files what the build left to serve as files, as paths inside the static folder
 * @param {RoutePart[][][]} onDemand the segments of each route rendered on demand
 * @param {{ fn: string, wholeFolders?: boolean, always?: string[] }} options
 *   fn is the function's name. wholeFolders lets a folder no on-demand route
 *   reaches go to files with one route, at the cost of Pail's own page for a
 *   path under it that has no file. always are folders that go to files
 *   whole whatever the routes say: the build's own assets.
 * @returns {Route[]}
 */
export function pailRoutes(files, onDemand, { fn, wholeFolders = true, always = [] }) {
  /** @type {Route[]} */
  const routes = [];
  const seen = new Set();
  const add = (path) => {
    if (seen.has(path)) return;
    seen.add(path);
    routes.push({ path, to: 'static' });
  };

  const walk = (/** @type {Folder} */ folder, /** @type {string[]} */ prefix) => {
    const whole = wholeFolders && !onDemand.some((segments) => reaches(segments, prefix));
    if (prefix.length > 0 && (whole || always.includes(prefix.join('/')))) {
      add(`/${prefix.join('/')}/*`);
      return;
    }
    for (const file of [...folder.files].sort()) add(pagePath(prefix, file));
    for (const name of [...folder.folders.keys()].sort()) walk(folder.folders.get(name), [...prefix, name]);
  };
  walk(tree(files), []);

  routes.push({ path: '/*', to: `function:${fn}` });
  return routes;
}

/** @typedef {{ files: string[], folders: Map<string, Folder> }} Folder */

/** @param {string[]} files */
function tree(files) {
  /** @type {Folder} */
  const root = { files: [], folders: new Map() };
  for (const file of files) {
    // A star anywhere but the end of a route's path is one Pail refuses.
    if (file.includes('*')) continue;
    const parts = file.split('/');
    const name = parts.pop();
    let folder = root;
    for (const part of parts) {
      if (!folder.folders.has(part)) folder.folders.set(part, { files: [], folders: new Map() });
      folder = folder.folders.get(part);
    }
    folder.files.push(name);
  }
  return root;
}

/**
 * The path a file is asked for at. Pail serves a page at its address without
 * the .html: about.html and about/index.html both answer /about.
 */
function pagePath(prefix, file) {
  if (file === 'index.html') return `/${prefix.join('/')}`;
  return `/${[...prefix, file.endsWith('.html') ? file.slice(0, -'.html'.length) : file].join('/')}`;
}

/**
 * Whether a route could answer a folder's own path, or one under it.
 * @param {RoutePart[][]} segments
 * @param {string[]} prefix
 */
export function reaches(segments, prefix) {
  for (let i = 0; i < prefix.length; i++) {
    const segment = segments[i];
    if (!segment) return false;
    if (segment.some((part) => part.spread)) return true;
    if (segment.some((part) => part.dynamic)) continue;
    if (
      segment
        .map((part) => part.content)
        .join('')
        .toLowerCase() !== prefix[i].toLowerCase()
    )
      return false;
  }
  return true;
}
