import { type FormEvent, useEffect, useState } from 'react';
import { connectGit, createFromRepo, type Detection, detectRepo, type GitHost, listRepos, type Repo } from '../api';
import { Button, Field } from '../ds';
import { message } from '../hooks';
import { slug, validName } from '../pack';
import { navigate, pailPath } from '../router';

// How many repos to look inside at once, and how many in all. Looking costs
// the git host a few requests each.
const LOOK_AT_ONCE = 4;
const LOOK_AT_MOST = 40;

// FromGit is New pail from a git host: connect it with a token if it isn't
// yet, then pick a repo.
export function FromGit(props: { git: GitHost; host: string; onConnected: () => void }) {
  return props.git.connected ? (
    <PickRepo git={props.git} host={props.host} />
  ) : (
    <Connect git={props.git} onConnected={props.onConnected} />
  );
}

function Connect({ git, onConnected }: { git: GitHost; onConnected: () => void }) {
  const [server, setServer] = useState(git.default_server);
  const [token, setToken] = useState('');
  const [error, setError] = useState('');
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

  return (
    <section className="pl-stack" style={{ gap: 16, maxWidth: 560 }}>
      <h2 className="pl-h2">Connect {git.label}</h2>
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
          <Button variant="primary" type="submit" disabled={busy || !token.trim()}>
            Connect
          </Button>
        </div>
      </form>
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

  // List the repos, then look inside each for what Pail would serve.
  useEffect(() => {
    let live = true;
    (async () => {
      let list: Repo[];
      try {
        list = await listRepos(git.kind);
      } catch (err) {
        if (live) setError(message(err));
        return;
      }
      if (!live) return;
      setRepos(list);
      const queue = list.slice(0, LOOK_AT_MOST);
      const look = async () => {
        for (let repo = queue.shift(); repo && live; repo = queue.shift()) {
          const full = repo.full;
          const result = await detectRepo(git.kind, full, repo.branch).catch(
            (): Detection => ({ deployable: false, summary: 'Pail couldn’t look inside this repo' }),
          );
          if (live) setFound((prev) => ({ ...prev, [full]: result }));
        }
      };
      await Promise.all(Array.from({ length: LOOK_AT_ONCE }, look));
    })();
    return () => {
      live = false;
    };
  }, [git.kind]);

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
        <span className="pl-small">Connected with a token{git.account ? ` as ${git.account}` : ''}</span>
      </div>

      {repos?.length === 0 && (
        <p className="pl-muted">This token can’t see any repos on {git.label}. Check what it has access to.</p>
      )}

      {repos && repos.length > 0 && (
        <div className="pl-stack" style={{ gap: 8 }} role="radiogroup" aria-label="Repository">
          {repos.map((repo) => {
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
