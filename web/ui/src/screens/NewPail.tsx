import { useCallback, useEffect, useState } from 'react';
import { type GitConnection, type GitHost, getConnection, type Info, listGitHosts, listPails } from '../api';
import { Button, Command, Field, SourcePicker } from '../ds';
import { type Upload, validName } from '../pack';
import { navigate, pailPath } from '../router';
import { UploadZone } from '../UploadZone';
import { FromGit } from './FromGit';

export function NewPail({ host, info }: { host: string; info: Info | null }) {
  // Coming back from a git host's sign-in, the address says which host it
  // was, and the connection the sign-in made or what went wrong there.
  const [arrived] = useState(() => new URLSearchParams(window.location.search));
  const [source, setSource] = useState<string | null>(arrived.get('source'));
  const [git, setGit] = useState<GitHost[]>([]);
  // The connections made here, by host. Each is for the one pail this page
  // makes: the next New pail connects again.
  const [connections, setConnections] = useState<Record<string, GitConnection | null>>({});
  const [arriving, setArriving] = useState(() => arrived.has('connection'));

  const setConnection = useCallback(
    (kind: string, conn: GitConnection | null) => setConnections((prev) => ({ ...prev, [kind]: conn })),
    [],
  );

  useEffect(() => {
    listGitHosts().then(setGit, () => {});
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

  const gitHost = git.find((g) => g.kind === source);

  return (
    <main className="pl-main pl-main-narrow">
      <div className="pl-stack" style={{ gap: 8 }}>
        <button type="button" className="pl-back" onClick={() => navigate('/')}>
          ← All pails
        </button>
        <h1 className="pl-display">New pail</h1>
      </div>

      <section className="pl-stack">
        <h2 className="pl-h2">Where is your site?</h2>
        <SourcePicker value={source} onChange={setSource} />
      </section>

      {source === 'cli' && <FromCli />}
      {source === 'upload' && <FromUpload host={host} info={info} />}
      {gitHost && !(arriving && gitHost.kind === arrived.get('source')) && (
        <FromGit
          key={gitHost.kind}
          git={gitHost}
          host={host}
          problem={gitHost.kind === arrived.get('source') ? (arrived.get('error') ?? undefined) : undefined}
          connection={connections[gitHost.kind] ?? null}
          onConnection={(conn) => setConnection(gitHost.kind, conn)}
        />
      )}
    </main>
  );
}

const RELEASES = 'https://github.com/chrisdmacrae/pail/releases/latest';

function FromCli() {
  return (
    <section className="pl-stack">
      <h2 className="pl-h2">Run it from your terminal or CI</h2>
      <p className="pl-muted">Get pail-cli with Homebrew, on a Mac or on Linux:</p>
      <Command>brew install chrisdmacrae/tap/pail</Command>
      <p className="pl-small">
        No Homebrew, or on Windows?{' '}
        <a className="pl-url" style={{ font: 'inherit' }} href={RELEASES} target="_blank" rel="noreferrer">
          Download it from the latest release
        </a>{' '}
        and put it somewhere on your PATH.
      </p>
      <p className="pl-muted">Point it at this Pail once:</p>
      <Command comment="asks for this Pail’s token">{`pail login ${window.location.origin}`}</Command>
      <p className="pl-muted">Then, from the folder you build:</p>
      <Command comment="the folder you’re in names the pail">pail up ./dist</Command>
      <Command comment="or name it yourself">pail up ./dist --name blog</Command>
      <p className="pl-small">Your pail shows up in the list as soon as the command finishes.</p>
      <div className="pl-actions">
        <Button onClick={() => navigate('/')}>Back to pails</Button>
      </div>
    </section>
  );
}

function FromUpload({ host, info }: { host: string; info: Info | null }) {
  const [name, setName] = useState('my-site');
  const [typed, setTyped] = useState(false);
  const [existing, setExisting] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listPails().then(
      (pails) => setExisting(pails.map((p) => p.name)),
      () => {},
    );
  }, []);

  // A name you typed wins; otherwise the folder or file names the pail.
  const pailFor = (upload: Upload) => {
    const pail = typed ? name : upload.suggested || name;
    if (!validName(pail)) {
      throw new Error(`Pail can’t use “${pail}” as a name. Use lowercase letters, numbers and dashes, like my-site.`);
    }
    setName(pail);
    return pail;
  };

  const nameError = typed && !validName(name) ? 'Use lowercase letters, numbers and dashes, like my-site.' : undefined;
  const hint = existing.includes(name)
    ? `${name} is already a pail. Dropping here deploys to it.`
    : 'Lowercase letters, numbers and dashes. Pail uses the folder name if you leave it.';

  return (
    <section className="pl-stack" style={{ gap: 16 }}>
      <h2 className="pl-h2">Drop it in</h2>
      <div style={{ maxWidth: 420 }}>
        <Field
          label="Name"
          mono
          suffix={`.${host}`}
          value={name}
          hint={hint}
          error={nameError}
          disabled={busy}
          onChange={(e) => {
            setName(e.target.value);
            setTyped(true);
          }}
        />
      </div>
      <UploadZone info={info} pailFor={pailFor} onBusy={setBusy} onDone={(pail) => navigate(pailPath(pail))} />
    </section>
  );
}
