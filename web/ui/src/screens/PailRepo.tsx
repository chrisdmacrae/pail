import { useEffect, useState } from 'react';
import { getPail, type Pail } from '../api';
import { SourcePicker } from '../ds';
import { navigate, pailPath } from '../router';
import { FromGit, useGitSources } from './FromGit';

// PailRepo gives a pail there already is a repo to deploy from: connect to a
// git host, then pick the repo, as New pail does. The pail keeps everything
// else it has.
export function PailRepo({ name, host }: { name: string; host: string }) {
  const { source, setSource, hosts, fromGit } = useGitSources();
  const [pail, setPail] = useState<Pail | null>(null);

  useEffect(() => {
    getPail(name).then(setPail, () => {});
  }, [name]);

  return (
    <main className="pl-main pl-main-narrow">
      <div className="pl-stack" style={{ gap: 8 }}>
        <button type="button" className="pl-back" onClick={() => navigate(pailPath(name))}>
          ← {name}
        </button>
        <h1 className="pl-display">Deploy {name} from a repo</h1>
        <p className="pl-muted" style={{ margin: 0 }}>
          {pail?.repo
            ? `${name} deploys from ${pail.repo} now. The repo you pick here takes its place.`
            : `Pail deploys the repo’s branch to ${name}, and again on every push.`}{' '}
          {name} keeps its name, addresses, variables and deploys.
        </p>
      </div>

      <section className="pl-stack">
        <h2 className="pl-h2">Where is the repo?</h2>
        <SourcePicker
          label="Where is the repo?"
          sources={hosts.map((h) => ({ id: h.kind, label: h.label, icon: 'git' as const }))}
          value={source}
          onChange={setSource}
        />
      </section>

      {fromGit && <FromGit key={fromGit.git.kind} host={host} pail={name} {...fromGit} />}
    </main>
  );
}
