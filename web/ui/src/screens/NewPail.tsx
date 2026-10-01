import { useCallback, useEffect, useState } from 'react';
import { type GitHost, type Info, listGitHosts, listPails } from '../api';
import { Button, Command, Field, SourcePicker } from '../ds';
import { type Upload, validName } from '../pack';
import { navigate, pailPath } from '../router';
import { UploadZone } from '../UploadZone';
import { FromGit } from './FromGit';

export function NewPail({ host, info }: { host: string; info: Info | null }) {
  const [source, setSource] = useState<string | null>(null);
  const [git, setGit] = useState<GitHost[]>([]);

  const loadGit = useCallback(() => listGitHosts().then(setGit, () => {}), []);
  useEffect(() => {
    loadGit();
  }, [loadGit]);

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
      {gitHost && <FromGit key={gitHost.kind} git={gitHost} host={host} onConnected={loadGit} />}
    </main>
  );
}

function FromCli() {
  return (
    <section className="pl-stack">
      <h2 className="pl-h2">Run it from your terminal or CI</h2>
      <p className="pl-muted">Get pail-cli: [INSTALL INSTRUCTIONS]. Point it at this Pail once:</p>
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
