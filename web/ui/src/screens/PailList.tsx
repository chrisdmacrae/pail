import { useEffect, useState } from 'react';
import { checkBaseDomain, type Info, listPails, type Pail, redeploy } from '../api';
import { Button, Command, PailRow } from '../ds';
import { message, usePoll } from '../hooks';
import { navigate, pailPath } from '../router';
import { ago, kept } from '../time';

export function PailList({ host, info }: { host: string; info: Info | null }) {
  const {
    data: pails,
    error,
    refresh,
  } = usePoll('pails', listPails, (list: Pail[]) => list.some((p) => p.status === 'building'));
  const [notice, setNotice] = useState('');
  // Quiet unless names under the base domain don't find their way here.
  const [dns, setDns] = useState('');

  useEffect(() => {
    checkBaseDomain().then(
      (check) => setDns(check.points_here ? '' : (check.detail ?? '')),
      () => {},
    );
  }, []);

  const live = pails?.filter((p) => p.status === 'live').length ?? 0;
  const count = pails?.length
    ? `${pails.length} ${pails.length === 1 ? 'pail' : 'pails'} on ${host} · ${live} live`
    : `Running on ${host}.`;

  const again = async (name: string) => {
    setNotice('');
    try {
      await redeploy(name);
    } catch (err) {
      setNotice(message(err));
    }
    refresh();
  };

  return (
    <main className="pl-main">
      <div className="pl-head">
        <div className="pl-stack" style={{ gap: 6 }}>
          <h1 className="pl-display">Your pails</h1>
          <p className="pl-muted">{count}</p>
        </div>
      </div>

      {(notice || (error && !pails)) && (
        <div className="pl-note pl-note-failed" role="alert">
          {notice || error?.message}
        </div>
      )}

      {dns && <div className="pl-note">{dns}</div>}

      {pails && pails.length > 0 && (
        <div className="pl-split">
          <div className="pl-stack">
            {pails.map((p) => (
              <PailRow
                key={p.name}
                name={p.name}
                url={p.host}
                href={p.url}
                status={p.status}
                source={p.source}
                updated={ago(p.updated_at)}
                onOpen={() => navigate(pailPath(p.name))}
                onRedeploy={() => again(p.name)}
                onRemove={() => navigate(pailPath(p.name), { confirm: true })}
              />
            ))}
          </div>
          <aside className="pl-card">
            <h2 className="pl-h2">Deploy from CI</h2>
            <p className="pl-small">
              Add one line to your pipeline after the build step. Same name, same pail: every run is a new deploy,
              {` and ${kept(info?.limits.max_deploys)} around to roll back to.`}
            </p>
            <Command comment="after npm run build">{`pail up ./dist --name ${pails[0].name}`}</Command>
          </aside>
        </div>
      )}

      {pails && pails.length === 0 && (
        <div
          style={{
            display: 'flex',
            flexDirection: 'column',
            alignItems: 'flex-start',
            gap: 16,
            maxWidth: 560,
            padding: '48px 0',
          }}
        >
          <h2 className="pl-title">Nothing in the pail yet.</h2>
          <p className="pl-muted">Run this from the folder you build, or press New pail to drop a folder.</p>
          <div style={{ alignSelf: 'stretch' }}>
            <Command>pail up ./dist</Command>
          </div>
          <Button variant="primary" icon="plus" onClick={() => navigate('/new')}>
            New pail
          </Button>
        </div>
      )}
      {info?.tls === 'internal' && (
        <p className="pl-small">
          Other devices need to trust this Pail once before pails open without a warning.{' '}
          <a
            className="pl-url"
            style={{ font: 'inherit' }}
            href="/trust"
            onClick={(e) => {
              e.preventDefault();
              navigate('/trust');
            }}
          >
            See how
          </a>
        </p>
      )}
    </main>
  );
}
