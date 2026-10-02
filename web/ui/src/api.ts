// The web UI is a plain client of Pail's REST API, like pail-cli. Every call
// sends the installation's token, which this browser keeps once it's given.

export type PailStatus = 'live' | 'building' | 'failed' | 'off';

export interface Deploy {
  id: string;
  state: 'building' | 'ok' | 'failed';
  source: string;
  label: string;
  created_at: string;
  finished_at?: string;
  error?: string;
  serving: boolean;
}

// Container is one container of the deploy a pail is serving.
export interface Container {
  name: string;
  port: number;
  state: 'running' | 'starting' | 'stopped';
}

// Fn is one function of the deploy a pail is serving. With no copies running
// it is asleep, and wakes on its next request.
export interface Fn {
  name: string;
  lang: string;
  copies: number;
  max: number;
}

// Route sends the paths it covers to files, a function or a container.
export interface Route {
  path: string;
  to: string;
}

export interface Pail {
  name: string;
  host: string;
  url: string;
  // hosts are its custom hostnames, beyond its own name.
  hosts: string[];
  status: PailStatus;
  // source is "cli", "upload", or the git host the pail deploys from.
  source: string;
  // repo and revision are the repository and branch of a pail from a git host.
  repo?: string;
  revision?: string;
  // dir is the folder of the repo the pail deploys from, when it isn't the top.
  dir?: string;
  serving: string;
  updated_at: string;
  deploy: Deploy | null;
  // containers are the microVMs of the deploy being served, if it has any.
  containers?: Container[];
  functions?: Fn[];
  // routes say what answers each path, for a pail with server code.
  routes?: Route[];
  // static and fallback are what pail.json says of the files being served:
  // the folder they are in, and the file that answers a path with none.
  static?: string;
  fallback?: string;
}

export interface Info {
  version: string;
  base_domain: string;
  // custom_hostnames says pails can take hostnames beyond the base domain.
  custom_hostnames: boolean;
  // tls is where certificates come from: Pail's own authority ("internal",
  // whose root each device trusts once), Let's Encrypt ("acme"), or none.
  tls: 'off' | 'internal' | 'acme';
  // secrets says this Pail has a key to seal a pail's secrets with.
  secrets?: boolean;
  limits: { max_upload_size: number; max_deploys: number; max_container_memory: number };
}

// GitHost is a git host a pail can come from.
export interface GitHost {
  kind: string;
  label: string;
  // self_hostable hosts need their server's address to connect.
  self_hostable: boolean;
  default_server: string;
  // oauth says the server has an OAuth app for this host, so people can
  // sign in instead of pasting a token.
  oauth: boolean;
}

// GitConnection is a connection to a git host, made for one pail and no
// other: the one about to be made, or one being reconnected. Pail holds its
// token; the id stands for it here.
export interface GitConnection {
  id: string;
  kind: string;
  account?: string;
  // via is how the connection was made.
  via: 'token' | 'oauth';
}

export interface Repo {
  // full is the repo with its owner: "homelab/recipes".
  full: string;
  // branch is its default branch.
  branch: string;
}

// Detection is what Pail makes of a repo before deploying it.
export interface Detection {
  deployable: boolean;
  summary: string;
}

// Host is one address a pail answers at, and whether its DNS reaches Pail.
export interface Host {
  host: string;
  url: string;
  default: boolean;
  points_here: boolean;
  detail?: string;
}

// Variable is one of a pail's variables, which pail.json uses as ${NAME}. A
// secret never comes with its value.
export interface Variable {
  name: string;
  value?: string;
  secret: boolean;
  updated_at: string;
}

export interface LogLine {
  time: string;
  text: string;
  level?: 'step' | 'ok' | 'error';
  // source is the container that printed the line, in a pail's output.
  source?: string;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

const TOKEN_KEY = 'pail.token';

// Storage can be blocked (private windows); the token then lasts for the page.
let token: string | null = null;
try {
  token = window.localStorage.getItem(TOKEN_KEY);
} catch {
  /* asked for again next visit */
}

export const hasToken = () => !!token;

export function setToken(value: string | null): void {
  token = value;
  try {
    if (value) window.localStorage.setItem(TOKEN_KEY, value);
    else window.localStorage.removeItem(TOKEN_KEY);
  } catch {
    /* kept for this page only */
  }
}

// Called when the server rejects the token, so the app can ask for it again.
let onRejected = () => {};
export function onTokenRejected(fn: () => void): void {
  onRejected = fn;
}

const UNREACHABLE = "Can't reach Pail. Check that it's running, then try again.";

async function toError(resp: Response): Promise<ApiError> {
  let code = 'error';
  let message = `Pail answered ${resp.status}.`;
  try {
    const body = await resp.json();
    if (body?.error?.message) ({ code, message } = body.error);
  } catch {
    /* not Pail's JSON; keep the status */
  }
  if (resp.status === 401) {
    setToken(null);
    onRejected();
  }
  return new ApiError(resp.status, code, message);
}

async function call(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<Response> {
  let resp: Response;
  try {
    resp = await fetch(`/api/v1${path}`, {
      method,
      signal,
      headers: {
        Authorization: `Bearer ${token ?? ''}`,
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') throw err;
    throw new ApiError(0, 'unreachable', UNREACHABLE);
  }
  if (!resp.ok) throw await toError(resp);
  return resp;
}

async function json<T>(method: string, path: string, body?: unknown): Promise<T> {
  return (await call(method, path, body)).json() as Promise<T>;
}

const pailPath = (name: string) => `/pails/${encodeURIComponent(name)}`;

export const getInfo = () => json<Info>('GET', '/info');
export const listPails = async () => (await json<{ pails: Pail[] }>('GET', '/pails')).pails;
export const getPail = (name: string) => json<Pail>('GET', pailPath(name));
export const listDeploys = async (name: string) =>
  (await json<{ deploys: Deploy[] }>('GET', `${pailPath(name)}/deploys`)).deploys;
export const redeploy = (name: string) => json<Deploy>('POST', `${pailPath(name)}/redeploy`);
export const serveDeploy = (name: string, deploy: string) => json<Pail>('POST', `${pailPath(name)}/serve`, { deploy });
export const stopPail = (name: string) => json<Pail>('POST', `${pailPath(name)}/stop`);
export const startPail = (name: string) => json<Pail>('POST', `${pailPath(name)}/start`);
export const listHosts = async (name: string) =>
  (await json<{ hosts: Host[] }>('GET', `${pailPath(name)}/hosts`)).hosts;
export const addHost = (name: string, host: string) => json<Host>('POST', `${pailPath(name)}/hosts`, { host });
export const removeHost = async (name: string, host: string) =>
  void (await call('DELETE', `${pailPath(name)}/hosts/${encodeURIComponent(host)}`));
export const listVariables = async (name: string) =>
  (await json<{ variables: Variable[] }>('GET', `${pailPath(name)}/env`)).variables;
export const setVariable = (name: string, key: string, value: string, secret: boolean) =>
  json<Variable>('PUT', `${pailPath(name)}/env/${encodeURIComponent(key)}`, { value, secret });
export const removeVariable = async (name: string, key: string) =>
  void (await call('DELETE', `${pailPath(name)}/env/${encodeURIComponent(key)}`));
export const listGitHosts = async () => (await json<{ hosts: GitHost[] }>('GET', '/git')).hosts;
export const connectGit = (kind: string, token: string, server?: string) =>
  json<GitConnection>('PUT', `/git/${kind}`, { token, server });
// getConnection is the connection a sign-in came back with.
export const getConnection = (kind: string, id: string) =>
  json<GitConnection>('GET', `/git/${kind}/connections/${encodeURIComponent(id)}`);
export const dropConnection = async (conn: GitConnection) =>
  void (await call('DELETE', `/git/${conn.kind}/connections/${encodeURIComponent(conn.id)}`));
// startSignIn begins signing in to a git host and returns the host's address
// to send the browser to. It comes back to New pail or, when the sign-in is
// to reconnect a pail, to that pail's page. With repo, the sign-in is to give
// that pail a repo to deploy from, and comes back to where one is picked.
export const startSignIn = async (kind: string, pail?: string, repo = false) =>
  (await json<{ url: string }>('POST', `/git/${kind}/oauth`, pail ? { pail, repo } : undefined)).url;
export const listRepos = async (conn: GitConnection) =>
  (await json<{ repos: Repo[] }>('GET', `/git/${conn.kind}/repos?connection=${encodeURIComponent(conn.id)}`)).repos;
// detectRepo looks at the top of a repo, or with dir, at that folder of it.
export const detectRepo = (conn: GitConnection, repo: string, branch: string, dir = '') =>
  json<Detection>(
    'GET',
    `/git/${conn.kind}/detect?connection=${encodeURIComponent(conn.id)}&repo=${encodeURIComponent(repo)}&branch=${encodeURIComponent(branch)}${dir ? `&dir=${encodeURIComponent(dir)}` : ''}`,
  );
// createFromRepo makes a pail from a repo, and makes the connection that
// pail's own. hook_note says so when Pail couldn't add the webhook that makes
// pushes deploy.
export const createFromRepo = (name: string, conn: GitConnection, repo: string, branch: string, dir = '') =>
  json<Deploy & { hook: boolean; hook_note?: string }>('POST', `${pailPath(name)}/repo`, {
    host: conn.kind,
    connection: conn.id,
    repo,
    branch,
    dir,
  });
// connectRepo has a pail there already is deploy from a repo from here on,
// in place of pail up, uploads, or the repo it deployed from before. The
// connection becomes the pail's own.
export const connectRepo = (name: string, conn: GitConnection, repo: string, branch: string, dir = '') =>
  json<Deploy & { hook: boolean; hook_note?: string }>('PUT', `${pailPath(name)}/repo`, {
    host: conn.kind,
    connection: conn.id,
    repo,
    branch,
    dir,
  });
// disconnectRepo has a pail stop deploying from its repo. It keeps serving
// and keeps its deploys, and is deployed by hand from then on.
export const disconnectRepo = (name: string) => json<Pail>('DELETE', `${pailPath(name)}/repo`);
// reconnectPail gives a pail from a git host a new connection, in place of
// the one it pulls with. It answers with whose the new one is.
export const reconnectPail = (name: string, connection: string) =>
  json<Omit<GitConnection, 'id'>>('PUT', `${pailPath(name)}/connection`, { connection });
// checkBaseDomain asks Pail whether names under its base domain reach it.
export const checkBaseDomain = () => json<{ points_here: boolean; detail?: string }>('GET', '/check');
export const removePail = async (name: string) => void (await call('DELETE', pailPath(name)));

// uploadDeploy sends an archive as a new deploy of name. It uses
// XMLHttpRequest because fetch can't report how much has been sent.
export function uploadDeploy(
  name: string,
  archive: Blob,
  fileName: string,
  onProgress: (fraction: number) => void,
): Promise<Deploy> {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open('POST', `/api/v1${pailPath(name)}/deploys?source=upload&file=${encodeURIComponent(fileName)}`);
    xhr.setRequestHeader('Authorization', `Bearer ${token ?? ''}`);
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
    xhr.onerror = () => reject(new ApiError(0, 'unreachable', UNREACHABLE));
    xhr.onload = () => {
      let body: (Partial<Deploy> & { error?: { code?: string; message?: string } }) | null = null;
      try {
        body = JSON.parse(xhr.responseText);
      } catch {
        /* handled below */
      }
      if (xhr.status >= 200 && xhr.status < 300 && body?.id) return resolve(body as Deploy);
      if (xhr.status === 401) {
        setToken(null);
        onRejected();
      }
      reject(
        new ApiError(xhr.status, body?.error?.code ?? 'error', body?.error?.message ?? `Pail answered ${xhr.status}.`),
      );
    };
    xhr.send(archive);
  });
}

// parseEvents pulls complete server-sent events out of buffer and returns
// what's left over: the start of an event still arriving.
export function parseEvents(buffer: string, emit: (event: string, data: string) => void): string {
  for (;;) {
    const end = buffer.indexOf('\n\n');
    if (end < 0) return buffer;
    let event = 'message';
    const data: string[] = [];
    for (const line of buffer.slice(0, end).split('\n')) {
      if (line.startsWith('event: ')) event = line.slice(7);
      else if (line.startsWith('data: ')) data.push(line.slice(6));
    }
    emit(event, data.join('\n'));
    buffer = buffer.slice(end + 2);
  }
}

// streamLog reads a deploy's log, live while it's building. It resolves with
// the finished deploy. EventSource can't send the token, so this reads the
// event stream by hand.
export async function streamLog(
  name: string,
  deploy: string,
  onLine: (line: LogLine) => void,
  signal: AbortSignal,
): Promise<Deploy | null> {
  const resp = await call('GET', `${pailPath(name)}/deploys/${encodeURIComponent(deploy)}/log`, undefined, signal);
  if (!resp.body) return null;
  const reader = resp.body.pipeThrough(new TextDecoderStream()).getReader();
  let done: Deploy | null = null;
  let buffer = '';
  for (;;) {
    const chunk = await reader.read();
    if (chunk.done) return done;
    buffer = parseEvents(buffer + chunk.value, (event, data) => {
      if (event === 'line') onLine(JSON.parse(data) as LogLine);
      if (event === 'done') done = JSON.parse(data) as Deploy;
    });
  }
}

// streamOutput reads what a pail's containers print: the lines Pail has kept,
// then each new one, until the signal aborts it.
export async function streamOutput(name: string, onLine: (line: LogLine) => void, signal: AbortSignal): Promise<void> {
  const resp = await call('GET', `${pailPath(name)}/output`, undefined, signal);
  if (!resp.body) return;
  const reader = resp.body.pipeThrough(new TextDecoderStream()).getReader();
  let buffer = '';
  for (;;) {
    const chunk = await reader.read();
    if (chunk.done) return;
    buffer = parseEvents(buffer + chunk.value, (event, data) => {
      if (event === 'line') onLine(JSON.parse(data) as LogLine);
    });
  }
}
