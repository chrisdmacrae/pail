import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ApiError,
  connectGit,
  connectRepo,
  createFromRepo,
  type Detection,
  detectRepo,
  dropConnection,
  type GitConnection,
  type GitHost,
  getConnection,
  listGitHosts,
  listRepos,
  type Repo,
  startSignIn,
} from '../api';
import { Button, Field } from '../ds';
import { message } from '../hooks';
import { slug, validName } from '../pack';
import { navigate, pailPath } from '../router';

// How many repos the list shows at a time.
const PER_PAGE = 12;
// How many repos to look inside at once. Looking costs the git host a few
// requests each, so only the page on screen is looked at.
const LOOK_AT_ONCE = 4;
// How long after the last keystroke a folder is looked inside.
const LOOK_AFTER_MS = 400;

// cleanDir is a folder as typed, without the ./ before it or the / after.
const cleanDir = (dir: string) =>
  dir
    .trim()
    .replace(/^(\.?\/)+/, '')
    .replace(/\/+$/, '');

// A connection waits a while for its pail, then Pail forgets it.
const lost = (err: unknown) => err instanceof ApiError && err.code === 'not_connected';

// useGitSources is what a screen that asks where a site is keeps: the source
// picked, the git hosts, and the connections made on the page. Each
// connection is for the one pail the page is about. Coming back from a git
// host's sign-in, the address says which host it was, and the connection the
// sign-in made or what went wrong there.
export function useGitSources() {
  const [arrived] = useState(() => new URLSearchParams(window.location.search));
  const [source, setSource] = useState<string | null>(arrived.get('source'));
  const [hosts, setHosts] = useState<GitHost[]>([]);
  const [connections, setConnections] = useState<Record<string, GitConnection | null>>({});
  const [arriving, setArriving] = useState(() => arrived.has('connection'));

  const setConnection = useCallback(
    (kind: string, conn: GitConnection | null) => setConnections((prev) => ({ ...prev, [kind]: conn })),
    [],
  );

  useEffect(() => {
    listGitHosts().then(setHosts, () => {});
  }, []);

  useEffect(() => {
    const kind = arrived.get('source');
    const id = arrived.get('connection');
    if (!kind || !id) return;
    let live = true;
    getConnection(kind, id)
      .then(
        (conn) => live && setConnection(kind, conn),
        () => {},
      )
      .finally(() => live && setArriving(false));
    // The connection is this page's now, and has no place in the address.
    window.history.replaceState(window.history.state, '', window.location.pathname);
    return () => {
      live = false;
    };
  }, [arrived, setConnection]);

  const git = hosts.find((g) => g.kind === source);
  const back = git?.kind === arrived.get('source');
  return {
    source,
    setSource,
    hosts,
    // What FromGit needs for the host that is picked, once a sign-in to it
    // has finished arriving.
    fromGit:
      git && !(arriving && back)
        ? {
            git,
            problem: back ? (arrived.get('error') ?? undefined) : undefined,
            connection: connections[git.kind] ?? null,
            onConnection: (conn: GitConnection | null) => setConnection(git.kind, conn),
          }
        : null,
  };
}

// FromGit is New pail from a git host: connect to it, by signing in or with
// a token, then pick a repo. The connection is for this pail alone. With
// pail, the repo is for that pail, which is there already.
export function FromGit(props: {
  git: GitHost;
  host: string;
  pail?: string;
  problem?: string;
  connection: GitConnection | null;
  onConnection: (conn: GitConnection | null) => void;
}) {
  const [ranOut, setRanOut] = useState(false);
  return props.connection ? (
    <PickRepo
      git={props.git}
      conn={props.connection}
      host={props.host}
      pail={props.pail}
      onLost={() => {
        setRanOut(true);
        props.onConnection(null);
      }}
      onChange={() => {
        setRanOut(false);
        props.onConnection(null);
      }}
    />
  ) : (
    <Connect
      git={props.git}
      pail={props.pail}
      forRepo={!!props.pail}
      problem={ranOut ? `Pail no longer has that connection to ${props.git.label}. Connect again.` : props.problem}
      onConnected={props.onConnection}
    />
  );
}

// Connect makes a connection to a git host, by signing in or with a token.
// It is for the pail about to be made or, with pail, for that pail: in place
// of the connection it has or, with forRepo, to pick it a repo with.
export function Connect({
  git,
  pail,
  forRepo,
  title,
  intro,
  problem,
  onConnected,
}: {
  git: GitHost;
  pail?: string;
  forRepo?: boolean;
  title?: string;
  intro?: string;
  problem?: string;
  // What is thrown here shows under the token, like a token the host refused.
  onConnected: (conn: GitConnection) => void | Promise<void>;
}) {
  const [server, setServer] = useState(git.default_server);
  const [token, setToken] = useState('');
  const [error, setError] = useState('');
  // What went wrong with a sign-in, here or at the git host.
  const [signInError, setSignInError] = useState(problem ?? '');
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    setBusy(true);
    try {
      await onConnected(await connectGit(git.kind, token, git.self_hostable ? server : undefined));
    } catch (err) {
      setError(message(err));
      setBusy(false);
    }
  };

  const signIn = async () => {
    setSignInError('');
    setBusy(true);
    try {
      // Off to the git host; it sends the browser back to where this is.
      window.location.assign(await startSignIn(git.kind, pail, forRepo));
    } catch (err) {
      setSignInError(message(err));
      setBusy(false);
    }
  };

  const setting = `PAIL_OAUTH_${git.kind.toUpperCase()}`;

  return (
    <section className="pl-stack" style={{ gap: 16, maxWidth: 560 }}>
      <h2 className="pl-h2">{title ?? `Connect ${git.label}`}</h2>
      {intro && (
        <p className="pl-muted" style={{ margin: 0 }}>
          {intro}
        </p>
      )}
      {signInError && (
        <div className="pl-note pl-note-failed" role="alert">
          {signInError}
        </div>
      )}
      {git.oauth && (
        <>
          <div className="pl-actions">
            <Button variant="primary" icon="git" disabled={busy} onClick={signIn}>
              Sign in with {git.label}
            </Button>
          </div>
          <div className="pl-or">or use a token</div>
        </>
      )}
      <form className="pl-card" onSubmit={submit}>
        {git.self_hostable && (
          <Field
            label="Server"
            mono
            placeholder="https://git.home.example"
            value={server}
            onChange={(e) => setServer(e.target.value)}
          />
        )}
        <Field
          label="Access token"
          type="password"
          mono
          autoComplete="off"
          placeholder="paste it here"
          value={token}
          error={error || undefined}
          hint="Needs read access to the repo and permission to add a webhook, so pushes deploy. Pail keeps it for this pail only."
          onChange={(e) => {
            setToken(e.target.value);
            setError('');
          }}
        />
        <div className="pl-actions">
          {/* One primary action: signing in, where the server offers it. */}
          <Button variant={git.oauth ? 'quiet' : 'primary'} type="submit" disabled={busy || !token.trim()}>
            Connect
          </Button>
        </div>
      </form>
      {!git.oauth && (
        <p className="pl-small">
          “Sign in with {git.label}” shows up here once this Pail has an OAuth app set up for {git.label}, with{' '}
          <code className="pl-mono">{setting}_CLIENT_ID</code> and{' '}
          <code className="pl-mono">{setting}_CLIENT_SECRET</code> on the server.
        </p>
      )}
    </section>
  );
}

function PickRepo({
  git,
  conn,
  host,
  pail,
  onLost,
  onChange,
}: {
  git: GitHost;
  conn: GitConnection;
  host: string;
  // The pail the repo is for, when it is one there already is.
  pail?: string;
  // The connection ran out before its pail was made.
  onLost: () => void;
  // The person wants to connect another way.
  onChange: () => void;
}) {
  const [repos, setRepos] = useState<Repo[] | null>(null);
  const [found, setFound] = useState<Record<string, Detection>>({});
  const [picked, setPicked] = useState<Repo | null>(null);
  const [name, setName] = useState('');
  // The folder of the repo the pail is in, as typed. Empty means the top.
  const [dir, setDir] = useState('');
  // What Pail found in a folder, keyed by repo and folder.
  const [inFolder, setInFolder] = useState<{ key: string; found: Detection } | null>(null);
  // Whether the name is the person's own, and so left alone.
  const [named, setNamed] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [page, setPage] = useState(0);
  // Repos already looked inside, or being looked at, so none is asked twice.
  const asked = useRef(new Set<string>());
  const whenLost = useRef(onLost);
  whenLost.current = onLost;

  useEffect(() => {
    let live = true;
    listRepos(conn).then(
      (list) => live && setRepos(list),
      (err) => live && (lost(err) ? whenLost.current() : setError(message(err))),
    );
    return () => {
      live = false;
    };
  }, [conn]);

  const pages = Math.max(1, Math.ceil((repos?.length ?? 0) / PER_PAGE));
  const shown = useMemo(() => (repos ?? []).slice(page * PER_PAGE, (page + 1) * PER_PAGE), [repos, page]);

  // Look inside the repos on this page for what Pail would serve.
  useEffect(() => {
    let live = true;
    const queue = shown.filter((repo) => !asked.current.has(repo.full));
    for (const repo of queue) asked.current.add(repo.full);
    const look = async () => {
      for (let repo = queue.shift(); repo; repo = queue.shift()) {
        const full = repo.full;
        const result = await detectRepo(conn, full, repo.branch).catch(
          (): Detection => ({ deployable: false, summary: 'Pail couldn’t look inside this repo' }),
        );
        if (live) setFound((prev) => ({ ...prev, [full]: result }));
        // Left the page before it was looked at: ask again next time.
        else asked.current.delete(full);
      }
    };
    Promise.all(Array.from({ length: LOOK_AT_ONCE }, look));
    return () => {
      live = false;
    };
  }, [conn, shown]);

  const folder = cleanDir(dir);
  const folderKey = picked && folder ? `${picked.full}:${folder}` : '';

  // Look inside the folder, once the typing stops.
  useEffect(() => {
    if (!picked || !folder) return;
    let live = true;
    const timer = setTimeout(async () => {
      const result = await detectRepo(conn, picked.full, picked.branch, folder).catch(
        (): Detection => ({ deployable: false, summary: 'Pail couldn’t look inside this folder' }),
      );
      if (live) setInFolder({ key: `${picked.full}:${folder}`, found: result });
    }, LOOK_AFTER_MS);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [conn, picked, folder]);

  // What Pail found where this pail would come from: undefined while it looks.
  const detected = !picked
    ? undefined
    : folder
      ? inFolder?.key === folderKey
        ? inFolder.found
        : undefined
      : found[picked.full];

  // A pail is named for its repo, and for its folder when it is one of several.
  const suggest = (repo: Repo, at: string) =>
    slug([repo.full.split('/').pop() ?? '', at.split('/').pop() ?? ''].filter(Boolean).join('-'));

  const pick = (repo: Repo) => {
    setPicked(repo);
    setDir('');
    setNamed(false);
    setName(suggest(repo, ''));
    setError('');
  };

  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (!picked || !detected?.deployable) return;
    setError('');
    setBusy(true);
    try {
      const made = pail
        ? await connectRepo(pail, conn, picked.full, picked.branch, folder)
        : await createFromRepo(name, conn, picked.full, picked.branch, folder);
      // If the webhook couldn't be added, the pail's page says so.
      navigate(pailPath(pail ?? name), made.hook_note ? { notice: made.hook_note } : undefined);
    } catch (err) {
      if (lost(err)) return onLost();
      setError(message(err));
      setBusy(false);
    }
  };

  const change = () => {
    dropConnection(conn).catch(() => {});
    onChange();
  };

  const nameError = name && !validName(name) ? 'Use lowercase letters, numbers and dashes, like my-site.' : undefined;

  return (
    <section className="pl-stack" style={{ gap: 16 }}>
      <div
        style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap' }}
      >
        <h2 className="pl-h2">Pick a repo on {git.label}</h2>
        <span className="pl-small" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          {conn.via === 'oauth' ? 'Signed in' : 'Connected with a token'}
          {conn.account ? ` as ${conn.account}` : ''}, for this pail only
          <Button size="sm" variant="quiet" disabled={busy} onClick={change}>
            Change
          </Button>
        </span>
      </div>

      {repos?.length === 0 && (
        <p className="pl-muted">This token can’t see any repos on {git.label}. Check what it has access to.</p>
      )}

      {repos && repos.length > 0 && (
        <div className="pl-repos">
          {/* With more than one page the list keeps its height, so the pager
              below doesn't jump when the last page is short. */}
          <div
            className={pages > 1 ? 'pl-repos-list is-paged' : 'pl-repos-list'}
            role="radiogroup"
            aria-label="Repository"
          >
            {shown.map((repo) => {
              const on = picked?.full === repo.full;
              const detected = found[repo.full];
              return (
                // biome-ignore lint/a11y/useSemanticElements: a row holds three pieces of text, which a radio input can't
                <button
                  key={repo.full}
                  type="button"
                  role="radio"
                  aria-checked={on}
                  className={on ? 'pl-repo is-on' : 'pl-repo'}
                  onClick={() => pick(repo)}
                >
                  <span style={{ flexGrow: 1, display: 'flex', flexDirection: 'column', minWidth: 0 }}>
                    <span className="pl-mono" style={{ fontWeight: 500, fontSize: 14, lineHeight: '20px' }}>
                      {repo.full}
                    </span>
                    <span className="pl-small">{detected ? detected.summary : 'Looking inside…'}</span>
                  </span>
                  <span className="pl-mono pl-small">{repo.branch}</span>
                </button>
              );
            })}
          </div>
          {pages > 1 && (
            <div className="pl-repos-pager">
              <span className="pl-small" aria-live="polite">
                {page * PER_PAGE + 1}–{page * PER_PAGE + shown.length} of {repos.length}
              </span>
              <div className="pl-actions">
                <Button size="sm" disabled={page === 0} onClick={() => setPage(page - 1)}>
                  Previous
                </Button>
                <Button size="sm" disabled={page >= pages - 1} onClick={() => setPage(page + 1)}>
                  Next
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      {picked && (
        <form className="pl-card" style={{ gap: 16 }} onSubmit={create}>
          <div style={{ maxWidth: 420 }}>
            <Field
              label="Folder"
              mono
              placeholder="apps/web"
              value={dir}
              hint={
                folder
                  ? (detected?.summary ?? 'Looking inside…')
                  : 'Leave empty for the top of the repo. In a repo with more than one pail, say which folder this one is in.'
              }
              onChange={(e) => {
                setDir(e.target.value);
                if (!named) setName(suggest(picked, cleanDir(e.target.value)));
                setError('');
              }}
            />
          </div>

          {detected?.deployable === false && (
            <div className="pl-note">
              <strong>Pail can’t deploy {folder ? `${folder} in ${picked.full}` : `the top of ${picked.full}`}.</strong>{' '}
              {detected.summary}.
              {!folder &&
                ' If the site is in a folder of the repo, such as docs or apps/web, type it in Folder above and Pail looks there.'}
            </div>
          )}

          {detected?.deployable && pail && (
            <>
              {error && (
                <div className="pl-note pl-note-failed" role="alert">
                  {error}
                </div>
              )}
              <p className="pl-small" style={{ margin: 0 }}>
                Pail deploys {picked.branch} to {pail} now, and again on every push to it.
              </p>
              <div className="pl-actions">
                <Button variant="primary" type="submit" disabled={busy}>
                  Deploy {pail} from this repo
                </Button>
              </div>
            </>
          )}

          {detected?.deployable && !pail && (
            <>
              <div style={{ maxWidth: 420 }}>
                <Field
                  label="Name"
                  mono
                  suffix={`.${host}`}
                  value={name}
                  error={nameError ?? (error || undefined)}
                  hint="Pail redeploys every push to the branch above. Add your own hostname after."
                  onChange={(e) => {
                    setName(e.target.value);
                    setNamed(true);
                    setError('');
                  }}
                />
              </div>
              <div className="pl-actions">
                <Button variant="primary" type="submit" disabled={busy || !validName(name)}>
                  Put it in the pail
                </Button>
              </div>
            </>
          )}
        </form>
      )}

      {error && !detected?.deployable && (
        <div className="pl-note pl-note-failed" role="alert">
          {error}
        </div>
      )}
    </section>
  );
}
