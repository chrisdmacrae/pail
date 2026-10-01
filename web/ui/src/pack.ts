// Packing an upload in the browser. A dropped .zip or .tar.gz goes to Pail as
// it is; a dropped folder, or a handful of loose files, is packed into a
// .tar.gz here first.

export interface Entry {
  // path inside the archive, with forward slashes and no leading one
  path: string;
  file: Blob;
}

// An Upload is ready to send, with the name Pail would guess for it.
export interface Upload {
  archive: Blob;
  fileName: string;
  // suggested is a pail name taken from the folder or file, or ''.
  suggested: string;
}

// What a build leaves lying around that is never part of a site.
const SKIP_FOLDERS = new Set(['.git', 'node_modules', '__MACOSX']);
const skipFile = (name: string) => name === '.DS_Store' || name.startsWith('._');

// slug makes a folder or file name into a pail name, or '' if nothing is left.
export function slug(name: string): string {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63)
    .replace(/-+$/, '');
}

export const validName = (name: string) => /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(name);

const ARCHIVE = /\.(zip|tar\.gz|tgz)$/i;

// fromFiles turns what a file picker gives into an upload.
export async function fromFiles(files: File[]): Promise<Upload> {
  if (files.length === 1 && ARCHIVE.test(files[0].name)) {
    const file = files[0];
    return { archive: file, fileName: file.name, suggested: slug(file.name.replace(ARCHIVE, '')) };
  }
  const entries = files.filter((f) => !skipFile(f.name)).map((f) => ({ path: f.name, file: f }));
  return { archive: await tarGz(entries), fileName: 'files', suggested: '' };
}

// collectDrop reads what was dropped. It must be called during the drop
// event, while the browser still lets the page see the items; the reading of
// folders then carries on after.
export function collectDrop(items: DataTransferItemList): Promise<Upload> | null {
  const roots: FileSystemEntry[] = [];
  for (const item of Array.from(items)) {
    const entry = item.kind === 'file' ? item.webkitGetAsEntry?.() : null;
    if (entry) roots.push(entry);
  }
  if (roots.length === 0) return null;

  return (async () => {
    if (roots.length === 1 && roots[0].isFile) {
      return fromFiles([await entryFile(roots[0] as FileSystemFileEntry)]);
    }
    const entries: Entry[] = [];
    for (const root of roots) await walk(root, '', entries);
    // One dropped folder is the site itself: its name is the pail's, and its
    // contents sit at the top of the archive.
    const folder = roots.length === 1 ? roots[0].name : '';
    const strip = folder ? folder.length + 1 : 0;
    return {
      archive: await tarGz(entries.map((e) => ({ path: e.path.slice(strip), file: e.file }))),
      fileName: folder || 'files',
      suggested: slug(folder),
    };
  })();
}

async function walk(entry: FileSystemEntry, prefix: string, out: Entry[]): Promise<void> {
  const path = prefix + entry.name;
  if (entry.isFile) {
    if (!skipFile(entry.name)) out.push({ path, file: await entryFile(entry as FileSystemFileEntry) });
    return;
  }
  if (SKIP_FOLDERS.has(entry.name)) return;
  const reader = (entry as FileSystemDirectoryEntry).createReader();
  // readEntries hands back a batch at a time, and an empty one when done.
  for (;;) {
    const batch = await new Promise<FileSystemEntry[]>((resolve, reject) => reader.readEntries(resolve, reject));
    if (batch.length === 0) return;
    for (const child of batch) await walk(child, `${path}/`, out);
  }
}

const entryFile = (entry: FileSystemFileEntry) => new Promise<File>((resolve, reject) => entry.file(resolve, reject));

// tarGz packs entries into a gzipped tar.
export async function tarGz(entries: Entry[]): Promise<Blob> {
  const parts: BlobPart[] = [];
  for (const { path, file } of entries) {
    parts.push(tarHeader(path, file.size), file);
    const pad = (512 - (file.size % 512)) % 512;
    if (pad) parts.push(new Uint8Array(pad));
  }
  parts.push(new Uint8Array(1024)); // two empty blocks end a tar
  const gz = new Blob(parts).stream().pipeThrough(new CompressionStream('gzip'));
  return new Response(gz).blob();
}

const utf8 = new TextEncoder();

// tarHeader is one ustar header block for a regular file.
function tarHeader(path: string, size: number): Uint8Array<ArrayBuffer> {
  let name = utf8.encode(path);
  let prefix = new Uint8Array(0);
  if (name.length > 100) {
    // ustar splits a long path at a slash: up to 155 bytes, then up to 100.
    const bytes = name;
    let cut = -1;
    for (let i = bytes.length - 101; i <= 155 && i < bytes.length; i++) {
      if (i >= 0 && bytes[i] === 0x2f) cut = i;
    }
    if (cut < 0) throw new Error(`${path} is too long a path for Pail to pack. Zip the folder and drop that.`);
    prefix = bytes.slice(0, cut);
    name = bytes.slice(cut + 1);
  }

  const block = new Uint8Array(512);
  const text = (value: string, at: number) => block.set(utf8.encode(value), at);
  const octal = (value: number, at: number, width: number) => text(value.toString(8).padStart(width - 1, '0'), at);
  block.set(name, 0);
  octal(0o644, 100, 8);
  octal(0, 108, 8);
  octal(0, 116, 8);
  octal(size, 124, 12);
  octal(Math.floor(Date.now() / 1000), 136, 12);
  text('        ', 148); // the checksum counts its own field as spaces
  text('0', 156);
  text('ustar\0' + '00', 257);
  block.set(prefix, 345);
  octal(
    block.reduce((sum, byte) => sum + byte, 0),
    148,
    7,
  );
  block[154] = 0;
  block[155] = 0x20;
  return block;
}
