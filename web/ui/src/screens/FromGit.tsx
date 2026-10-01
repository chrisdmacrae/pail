import { type FormEvent, useEffect, useMemo, useRef, useState } from 'react';
import {
  connectGit,
  createFromRepo,
  type Detection,
  detectRepo,
  type GitHost,
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

// FromGit is New pail from a git host: connect it with a token if it isn't
// yet, then pick a repo.
export function FromGit(props: { git: GitHost; host: string; problem?: string; onConnected: () => void }) {
  return props.git.connected ? (
    <PickRepo git={props.git} host={props.host} />
  ) : (
    <Connect git={props.git} problem={props.problem} onConnected={props.onConnected} />
  );
}

function Connect({ git, problem, onConnected }: { git: GitHost; problem?: string; onConnected: () => void }) {
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
      await connectGit(git.kind, token, git.self_hostable ? server : undefined);
      onConnected();
    } catch (err) {
      setError(message(err));
      setBusy(false);
    }
  };

  const signIn = async () => {
    setSignInError('');
    setBusy(true);
    try {
      // Off to the git host; it sends the browser back to New pail.
      window.location.assign(await startSignIn(git.kind));
    } catch (err) {
      setSignInError(message(err));
      setBusy(false);
    }
  };

  const setting = `PAIL_OAUTH_${git.kind.toUpperCase()}`;

  return (
    <section className="pl-stack" style={{ gap: 16, maxWidth: 560 }}>
      <h2 className="pl-h2">Connect {git.label}</h2>
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
          hint="Needs read access to your repos and permission to add a webhook, so pushes deploy."
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

function PickRepo({ git, host }: { git: GitHost; host: string }) {
  const [repos, setRepos] = useState<Repo[] | null>(null);
  const [found, setFound] = useState<Record<string, Detection>>({});
  const [picked, setPicked] = useState<Repo | null>(null);
  const [name, setName] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [page, setPage] = useState(0);
  // Repos already looked inside, or being looked at, so none is asked twice.
  const asked = useRef(new Set<string>());

  useEffect(() => {
    let live = true;
    listRepos(git.kind).then(
      (list) => live && setRepos(list),
      (err) => live && setError(message(err)),
    );
    return () => {
      live = false;
    };
  }, [git.kind]);

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
        const result = await detectRepo(git.kind, full, repo.branch).catch(
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
  }, [git.kind, shown]);

  const pick = (repo: Repo) => {
    setPicked(repo);
    setName(slug(repo.full.split('/').pop() ?? ''));
    setError('');
  };

  const create = async (e: FormEvent) => {
    e.preventDefault();
    if (!picked) return;
    setError('');
    setBusy(true);
    try {
      const made = await createFromRepo(name, git.kind, picked.full, picked.branch);
      // If the webhook couldn't be added, the pail's page says so.
      navigate(pailPath(name), made.hook_note ? { notice: made.hook_note } : undefined);
    } catch (err) {
      setError(message(err));
      setBusy(false);
    }
  };

  const nameError = name && !validName(name) ? 'Use lowercase letters, numbers and dashes, like my-site.' : undefined;

  return (
    <section className="pl-stack" style={{ gap: 16 }}>
      <div
        style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap' }}
      >
        <h2 className="pl-h2">Pick a repo on {git.label}</h2>
        <span className="pl-small">
          {git.via === 'oauth' ? 'Signed in' : 'Connected with a token'}
          {git.account ? ` as ${git.account}` : ''}
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

      {picked && found[picked.full]?.deployable === false && (
        <div className="pl-note">
          <strong>Pail can’t deploy {picked.full} yet.</strong> {found[picked.full].summary}. For now, Pail serves a
          repo’s files as they are: an index.html at the top, or a pail.json that says where the files live.
        </div>
      )}

      {picked && found[picked.full]?.deployable && (
        <form className="pl-card" style={{ gap: 16 }} onSubmit={create}>
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
                setError('');
              }}
            />
          </div>
          <div className="pl-actions">
            <Button variant="primary" type="submit" disabled={busy || !validName(name)}>
              Put it in the pail
            </Button>
          </div>
        </form>
      )}

      {error && !(picked && found[picked.full]?.deployable) && (
        <div className="pl-note pl-note-failed" role="alert">
          {error}
        </div>
      )}
    </section>
  );
}
