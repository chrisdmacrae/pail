import { Fragment, useEffect, useRef, useState } from 'react';
import {
  ApiError,
  type Deploy,
  disconnectRepo,
  dropConnection,
  type GitConnection,
  type GitHost,
  getPail,
  type Info,
  type LogLine,
  listDeploys,
  listGitHosts,
  type Pail,
  reconnectPail,
  redeploy,
  removePail,
  serveDeploy,
  startPail,
  stopPail,
  streamLog,
  streamOutput,
} from '../api';
import { BuildLog, Button, Command, Status } from '../ds';
import { message, usePoll } from '../hooks';
import { navigate, pailPath, routeState } from '../router';
import { ago, clock } from '../time';
import { UploadZone } from '../UploadZone';
import { Addresses } from './Addresses';
import { Connect } from './FromGit';
import { Variables } from './Variables';

const SOURCES: Record<string, string> = {
  cli: 'pail-cli',
  upload: 'Upload',
  github: 'GitHub',
  gitlab: 'GitLab',
  bitbucket: 'Bitbucket',
  gitea: 'Gitea',
  forgejo: 'Forgejo',
};

const CONTAINER_STATES = { running: 'Running', starting: 'Starting', stopped: 'Stopped' };

// How many lines of output the page keeps on screen.
const OUTPUT_LINES = 300;

// terminalCommand is how to deploy this pail again from a terminal. A pail
// with containers is deployed from its project's folder, not a built one.
function terminalCommand(pail: Pail): string {
  if (pail.repo) return `pail redeploy ${pail.name}`;
  if (pail.containers?.length) return `pail up --name ${pail.name}`;
  return `pail up ./dist --name ${pail.name}`;
}

// A pail's git host turned its connection away, or it has none: its token
// ran out or was revoked, or the host ended its sign-in.
const needsConnection = (err: unknown) =>
  err instanceof ApiError && (err.code === 'token_rejected' || err.code === 'not_connected');

// reconnected says whose a pail's new connection is.
const reconnected = (pail: string, conn: Omit<GitConnection, 'id'>) =>
  `${pail} has a new connection to ${SOURCES[conn.kind] ?? conn.kind}${conn.account ? `, as ${conn.account}` : ''}. Redeploy pulls its branch with it.`;

const BackButton = () => (
  <button type="button" className="pl-back" onClick={() => navigate('/')}>
    ← All pails
  </button>
);

export function PailPage({ name, info }: { name: string; info: Info | null }) {
  const { data, error, refresh } = usePoll(
    name,
    async () => {
      const [pail, deploys] = await Promise.all([getPail(name), listDeploys(name)]);
      return { pail, deploys };
    },
    // A container on its way up, or a function with copies awake, changes
    // without anyone pressing anything.
    ({ pail }: { pail: Pail }) =>
      pail.status === 'building' ||
      !!pail.containers?.some((c) => c.state === 'starting') ||
      !!pail.functions?.some((f) => f.copies > 0),
  );
  const [selected, setSelected] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(() => !!routeState<{ confirm?: boolean }>()?.confirm);
  // Something the last screen had to say about this pail, such as a webhook
  // that couldn't be added.
  const [notice, setNotice] = useState(() => routeState<{ notice?: string }>()?.notice ?? '');
  const [busy, setBusy] = useState(false);
  const [uploading, setUploading] = useState(false);
  // Asking whether to stop deploying from the pail's repo.
  const [leaving, setLeaving] = useState(false);
  // Coming back from a git host's sign-in to reconnect this pail, the address
  // carries the connection the sign-in made, or what went wrong there.
  const [arrived] = useState(() => new URLSearchParams(window.location.search));
  const [reconnecting, setReconnecting] = useState(() => arrived.has('error'));
  const [problem, setProblem] = useState(() => arrived.get('error') ?? '');
  // Good news about this pail, such as its new connection.
  const [said, setSaid] = useState('');
  // The git host this pail deploys from, if it comes from one.
  const [gitHost, setGitHost] = useState<GitHost | null>(null);
  const source = data?.pail.repo ? data.pail.source : '';
  // A connection can be given once, so it is given once.
  const given = useRef(false);

  useEffect(() => {
    if (!source) return;
    let live = true;
    listGitHosts().then(
      (hosts) => live && setGitHost(hosts.find((h) => h.kind === source) ?? null),
      () => {},
    );
    return () => {
      live = false;
    };
  }, [source]);

  useEffect(() => {
    const id = arrived.get('connection');
    if (!id && !arrived.has('error')) return;
    // What the sign-in came back with has no place in the address.
    window.history.replaceState(window.history.state, '', window.location.pathname);
    if (!id || given.current) return;
    given.current = true;
    reconnectPail(name, id).then(
      (conn) => setSaid(reconnected(name, conn)),
      (err) => {
        setProblem(message(err));
        setReconnecting(true);
      },
    );
  }, [arrived, name]);

  if (!data) {
    return (
      <main className="pl-main">
        <div className="pl-stack" style={{ gap: 8 }}>
          <BackButton />
          {error && (
            <>
              <h1 className="pl-title">{error.status === 404 ? name : 'Something went wrong'}</h1>
              <p className="pl-muted">{error.message}</p>
            </>
          )}
        </div>
      </main>
    );
  }

  const { pail, deploys } = data;
  const building = pail.status === 'building';
  const sel = deploys.find((d) => d.id === selected) ?? deploys[0];
  // What runs for this pail and may print something.
  const servers = [...(pail.containers ?? []), ...(pail.functions ?? [])].map((c) => c.name);

  // act runs one change against the API, then shows where things stand.
  const act = async (change: () => Promise<unknown>) => {
    setNotice('');
    setSaid('');
    setBusy(true);
    try {
      await change();
    } catch (err) {
      setNotice(message(err));
      // The pail can't pull: a new connection is what it needs.
      if (pail.repo && needsConnection(err)) setReconnecting(true);
    }
    setBusy(false);
    refresh();
  };

  const remove = async () => {
    setBusy(true);
    try {
      await removePail(name);
      navigate('/');
    } catch (err) {
      setNotice(message(err));
      setBusy(false);
    }
  };

  return (
    <main className="pl-main">
      <div className="pl-stack" style={{ gap: 8 }}>
        <BackButton />
        <div className="pl-head">
          <div className="pl-stack" style={{ gap: 6 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
              <h1 className="pl-title">{pail.name}</h1>
              <Status state={pail.status} />
            </div>
            <a className="pl-url" href={pail.url}>
              {pail.host} ↗
            </a>
          </div>
          <div className="pl-actions">
            {pail.status !== 'off' && (
              <Button disabled={busy} onClick={() => act(() => stopPail(name))}>
                Stop
              </Button>
            )}
            <Button
              icon="redeploy"
              disabled={busy || building}
              onClick={() =>
                act(async () => {
                  await redeploy(name);
                  setSelected(null);
                })
              }
            >
              Redeploy
            </Button>
            {pail.status === 'off' ? (
              <Button variant="primary" disabled={busy} onClick={() => act(() => startPail(name))}>
                Start
              </Button>
            ) : (
              <a className="pa-btn pa-btn-primary pl-link-btn" href={pail.url}>
                Open site
              </a>
            )}
          </div>
        </div>
      </div>

      {notice && (
        <div className="pl-note pl-note-failed" role="alert">
          {notice}
        </div>
      )}
      {said && (
        <div className="pl-note" role="status">
          {said}
        </div>
      )}
      {reconnecting && gitHost && (
        <Reconnect
          key={problem}
          pail={pail}
          git={gitHost}
          problem={problem || undefined}
          onDone={(conn) => {
            setReconnecting(false);
            setProblem('');
            setNotice('');
            setSaid(reconnected(pail.name, conn));
          }}
          onCancel={() => {
            setReconnecting(false);
            setProblem('');
          }}
        />
      )}
      {pail.status === 'failed' && (
        <div className="pl-note pl-note-failed">
          <strong>The last deploy failed.</strong>{' '}
          {pail.serving
            ? `${pail.host} is still serving ${pail.serving}. The log says why — or serve an older deploy below.`
            : `Nothing is live at ${pail.host} yet. The log says why.`}
        </div>
      )}
      {pail.status === 'off' && (
        <div className="pl-note">
          <strong>{pail.name} is off.</strong> {pail.host} answers nothing until you start it. Its deploys are kept.
        </div>
      )}

      <div className="pl-detail">
        <section className="pl-stack">
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
            <h2 className="pl-h2">Deploys</h2>
            <Button size="sm" icon={uploading ? undefined : 'upload'} onClick={() => setUploading(!uploading)}>
              {uploading ? 'Cancel' : 'Upload a deploy'}
            </Button>
          </div>
          {uploading && (
            <UploadZone
              info={info}
              hint={`It goes live as a new deploy of ${pail.name}. Static files with an index.html at the top.`}
              pailFor={() => name}
              onDone={() => {
                setUploading(false);
                setSelected(null);
                refresh();
              }}
            />
          )}
          <div className="pl-deploys">
            {deploys.map((d) => (
              <DeployRow
                key={d.id}
                deploy={d}
                selected={d.id === sel?.id}
                canServe={d.state === 'ok' && !d.serving && !building && !busy}
                onSelect={() => setSelected(d.id)}
                onServe={() => act(() => serveDeploy(name, d.id))}
              />
            ))}
          </div>
          {sel && <DeployLog key={sel.id} pail={name} deploy={sel} onFinished={refresh} />}
          <ServerCode pail={pail} />
          {servers.length > 0 && (
            <>
              <h2 className="pl-h2" style={{ marginTop: 16 }}>
                Output
              </h2>
              <p className="pl-small" style={{ margin: 0 }}>
                What {servers.length === 1 ? servers[0] : 'its server code'} printed lately, as it happens.
              </p>
              <Output pail={name} many={servers.length > 1} />
            </>
          )}
        </section>

        <aside style={{ display: 'flex', flexDirection: 'column', gap: 32, minWidth: 0 }}>
          <Addresses pail={pail} info={info} />

          <Variables pail={pail} info={info} />

          <dl className="pl-facts">
            <dt>Source</dt>
            <dd>{SOURCES[pail.source] ?? pail.source}</dd>
            {pail.repo && (
              <>
                <dt>Repo</dt>
                <dd className="pl-mono" style={{ fontSize: 13, overflowWrap: 'anywhere' }}>
                  {pail.repo}
                </dd>
                <dt>Branch</dt>
                <dd className="pl-mono" style={{ fontSize: 13 }}>
                  {pail.revision}
                </dd>
                {pail.dir && (
                  <>
                    <dt>Folder</dt>
                    <dd className="pl-mono" style={{ fontSize: 13, overflowWrap: 'anywhere' }}>
                      {pail.dir}
                    </dd>
                  </>
                )}
              </>
            )}
            <dt>Serving</dt>
            <dd className="pl-mono" style={{ fontSize: 13 }}>
              {pail.serving || '—'}
            </dd>
            {pail.containers?.map((c) => (
              <Fragment key={c.name}>
                <dt>{c.name}</dt>
                <dd>{`${CONTAINER_STATES[c.state]} · port ${c.port}`}</dd>
              </Fragment>
            ))}
          </dl>

          {pail.repo && gitHost && (
            <section className="pl-stack" style={{ gap: 8 }}>
              <p className="pl-small" style={{ margin: 0 }}>
                {pail.name} pulls {pail.repo} with a connection of its own. If its token has run out or been revoked,
                give it a new one.
              </p>
              <div className="pl-actions">
                <Button size="sm" disabled={reconnecting} onClick={() => setReconnecting(true)}>
                  Reconnect to {gitHost.label}
                </Button>
                <Button size="sm" variant="quiet" onClick={() => navigate(`${pailPath(name)}/git`)}>
                  Change repo
                </Button>
                <Button size="sm" variant="quiet" disabled={leaving} onClick={() => setLeaving(true)}>
                  Disconnect
                </Button>
              </div>
              {leaving && (
                <div className="pl-note pl-stack" style={{ padding: 16 }}>
                  <p style={{ margin: 0 }}>
                    <b>
                      Disconnect {pail.name} from {pail.repo}?
                    </b>{' '}
                    Pushes stop deploying it. Pail takes its webhook off the repo and forgets its connection to{' '}
                    {gitHost.label}. {pail.name} keeps serving, and keeps its deploys: from then on you deploy it with
                    pail up or an upload.
                  </p>
                  <div className="pl-actions">
                    <Button
                      variant="danger"
                      disabled={busy}
                      onClick={() =>
                        act(async () => {
                          const repo = pail.repo;
                          await disconnectRepo(name);
                          setLeaving(false);
                          setReconnecting(false);
                          setSaid(
                            `${pail.name} no longer deploys from ${repo}. Deploy it with pail up, or upload a deploy.`,
                          );
                        })
                      }
                    >
                      Disconnect
                    </Button>
                    <Button onClick={() => setLeaving(false)}>Keep it</Button>
                  </div>
                </div>
              )}
            </section>
          )}
          {!pail.repo && (
            <section className="pl-stack" style={{ gap: 8 }}>
              <p className="pl-small" style={{ margin: 0 }}>
                {pail.name} is deployed by hand. It can deploy from a git repo instead, on every push.
              </p>
              <div className="pl-actions">
                <Button size="sm" icon="git" onClick={() => navigate(`${pailPath(name)}/git`)}>
                  Deploy from a git repo
                </Button>
              </div>
            </section>
          )}

          <section className="pl-stack" style={{ gap: 8 }}>
            <h2 className="pl-h2" style={{ fontSize: 16, lineHeight: '22px' }}>
              Same thing, from a terminal
            </h2>
            <Command>{terminalCommand(pail)}</Command>
          </section>
        </aside>
      </div>

      <section className="pl-stack" style={{ paddingTop: 24, borderTop: '1px solid var(--line)' }}>
        {confirming ? (
          <div className="pl-note pl-note-failed pl-stack" style={{ padding: 16 }}>
            <p style={{ margin: 0 }}>
              <b>Remove {pail.name}?</b>
              {` ${pail.host}${pail.hosts.length ? ' and its hostnames stop' : ' stops'} answering, and every deploy is deleted. This can’t be undone.`}
            </p>
            <div className="pl-actions">
              <Button variant="danger" disabled={busy} onClick={remove}>
                Remove pail
              </Button>
              <Button onClick={() => setConfirming(false)}>Keep it</Button>
            </div>
          </div>
        ) : (
          <div className="pl-actions">
            <Button icon="trash" onClick={() => setConfirming(true)}>
              Remove pail
            </Button>
          </div>
        )}
      </section>
    </main>
  );
}

// Reconnect gives a pail from a git host a new connection, made the way New
// pail makes one, in place of the one it pulls with.
function Reconnect(props: {
  pail: Pail;
  git: GitHost;
  problem?: string;
  onDone: (conn: Omit<GitConnection, 'id'>) => void;
  onCancel: () => void;
}) {
  const { pail, git } = props;
  const top = useRef<HTMLElement>(null);

  // It opens above the button that asks for it, which may be off screen.
  useEffect(() => top.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }), []);

  const give = async (conn: GitConnection) => {
    try {
      props.onDone(await reconnectPail(pail.name, conn.id));
    } catch (err) {
      // It can't be this pail's, and was made for no other.
      dropConnection(conn).catch(() => {});
      throw err;
    }
  };

  return (
    <section ref={top} className="pl-stack" style={{ gap: 16 }}>
      <Connect
        git={git}
        pail={pail.name}
        title={`Reconnect ${pail.name} to ${git.label}`}
        intro={`The new connection takes the place of the one ${pail.name} pulls with, so it has to be able to see ${pail.repo}. Nothing else about the pail changes.`}
        problem={props.problem}
        onConnected={give}
      />
      <div className="pl-actions">
        <Button onClick={props.onCancel}>Cancel</Button>
      </div>
    </section>
  );
}

function DeployRow(props: {
  deploy: Deploy;
  selected: boolean;
  canServe: boolean;
  onSelect: () => void;
  onServe: () => void;
}) {
  const d = props.deploy;
  return (
    <div className={props.selected ? 'pl-deploy is-sel' : 'pl-deploy'}>
      <button type="button" className="pl-deploy-pick" onClick={props.onSelect} aria-pressed={props.selected}>
        <span className="pl-deploy-id pl-mono" style={{ fontWeight: 500, fontSize: 13, lineHeight: '20px' }}>
          {d.id}
        </span>
        <span
          className="pl-deploy-meta"
          style={{
            flexGrow: 1,
            minWidth: 0,
            fontSize: 13,
            color: 'var(--ink-muted)',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
          }}
        >
          {d.label}
        </span>
        <span style={{ fontSize: 13, color: 'var(--ink-muted)', whiteSpace: 'nowrap' }}>{ago(d.created_at)}</span>
      </button>
      {d.serving && <span className="pl-tag pl-tag-serving">Serving</span>}
      {d.state === 'failed' && <Status state="failed" />}
      {d.state === 'building' && <Status state="building" />}
      {props.canServe && (
        <Button size="sm" onClick={props.onServe}>
          Serve this one
        </Button>
      )}
    </div>
  );
}

// DeployLog shows one deploy's log, following it live while it builds.
function DeployLog(props: { pail: string; deploy: Deploy; onFinished: () => void }) {
  const { pail, deploy, onFinished } = props;
  const [lines, setLines] = useState<LogLine[]>([]);
  // There is one stream per deploy, however often the page around it
  // refreshes, so the stream reads these as they are when it needs them.
  const latest = useRef({ state: deploy.state, onFinished });
  latest.current = { state: deploy.state, onFinished };

  useEffect(() => {
    const wasBuilding = latest.current.state === 'building';
    const stop = new AbortController();
    setLines([]);
    streamLog(pail, deploy.id, (line) => setLines((prev) => [...prev, line]), stop.signal).then(
      () => wasBuilding && !stop.signal.aborted && latest.current.onFinished(),
      () => {}, // the page's own polling reports what went wrong
    );
    return () => stop.abort();
  }, [pail, deploy.id]);

  return <BuildLog lines={lines.map((l) => ({ time: clock(l.time), text: l.text, level: l.level }))} />;
}

const PAIL_JSON = `{
  "functions": { "api": { "src": "./fn/api" } },
  "routes": [
    { "path": "/api/*", "to": "function:api" },
    { "path": "/*", "to": "static", "fallback": "index.html" }
  ]
}`;

// ServerCode shows what the serving deploy's pail.json set up: where its
// files are, which paths go where, and its functions. With none of that, it
// says how to add some.
function ServerCode({ pail }: { pail: Pail }) {
  const functions = pail.functions ?? [];
  const label = { fontSize: 12, fontWeight: 600, letterSpacing: '.03em' };
  const heading = (
    <h2 className="pl-h2" style={{ marginTop: 16 }}>
      Functions and routes
    </h2>
  );
  const routes = pail.routes ?? [];
  // What pail.json says of the files, which a pail with no server code may
  // still have.
  const files = [
    pail.static && { from: `./${pail.static}`, what: 'the folder the files are in' },
    pail.fallback && { from: pail.fallback, what: 'answers a path with no file of its own' },
  ].filter((f) => !!f);
  const row = { display: 'flex', gap: 12, alignItems: 'baseline', flexWrap: 'wrap', fontSize: 13 } as const;
  if (routes.length === 0 && files.length === 0) {
    return (
      <>
        {heading}
        <div className="pl-card">
          <p className="pl-muted" style={{ margin: 0 }}>
            No server code, so Pail serves the files as they are. To run some, put a <code>pail.json</code> beside your
            app:
          </p>
          <pre className="pl-code">{PAIL_JSON}</pre>
        </div>
      </>
    );
  }
  return (
    <>
      {heading}
      <div className="pl-card" style={{ gap: 16 }}>
        <p className="pl-small" style={{ margin: 0 }}>
          From <code>pail.json</code> in deploy {pail.serving}. Change it in your project; Pail reads it again on every
          deploy.
        </p>
        {files.length > 0 && (
          <div className="pl-stack" style={{ gap: 8 }}>
            <span style={label}>FILES</span>
            {files.map((f) => (
              <div key={f.what} style={row}>
                <span className="pl-mono" style={{ minWidth: 120 }}>
                  {f.from}
                </span>
                <span style={{ color: 'var(--ink-muted)' }}>{f.what}</span>
              </div>
            ))}
          </div>
        )}
        {routes.length > 0 && (
          <div className="pl-stack" style={{ gap: 8 }}>
            <span style={label}>ROUTES</span>
            {routes.map((r) => (
              <div key={r.path} className="pl-mono" style={row}>
                <span style={{ minWidth: 120 }}>{r.path}</span>
                <span style={{ color: 'var(--ink-muted)' }}>→ {r.to}</span>
              </div>
            ))}
          </div>
        )}
        {functions.length > 0 && (
          <div className="pl-stack" style={{ gap: 8 }}>
            <span style={label}>FUNCTIONS</span>
            {functions.map((f) => (
              <div key={f.name} style={row}>
                <span className="pl-mono" style={{ minWidth: 120 }}>
                  {f.name}
                </span>
                <span style={{ color: 'var(--ink-muted)' }}>
                  {`${LANGS[f.lang] ?? f.lang} · ${f.copies === 0 ? 'asleep' : `${f.copies} of ${f.max} awake`}`}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </>
  );
}

const LANGS: Record<string, string> = {
  python: 'Python',
  node: 'Node',
  ruby: 'Ruby',
  go: 'Go',
  rust: 'Rust',
  shell: 'Shell',
};

// Output shows what a pail's containers print, live. With more than one
// container, each line says whose it is.
function Output({ pail, many }: { pail: string; many: boolean }) {
  const [lines, setLines] = useState<LogLine[]>([]);

  useEffect(() => {
    const stop = new AbortController();
    setLines([]);
    streamOutput(pail, (line) => setLines((prev) => [...prev, line].slice(-OUTPUT_LINES)), stop.signal).catch(() => {});
    return () => stop.abort();
  }, [pail]);

  if (lines.length === 0) return <p className="pl-muted">Nothing printed yet.</p>;
  return (
    <BuildLog
      lines={lines.map((l) => ({
        time: clock(l.time),
        text: many && l.source && !l.level ? `${l.source}: ${l.text}` : l.text,
        level: l.level,
      }))}
    />
  );
}
