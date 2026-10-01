import { useEffect, useRef, useState } from 'react';
import {
  type Deploy,
  getPail,
  type Info,
  type LogLine,
  listDeploys,
  type Pail,
  redeploy,
  removePail,
  serveDeploy,
  startPail,
  stopPail,
  streamLog,
} from '../api';
import { BuildLog, Button, Command, Status } from '../ds';
import { message, usePoll } from '../hooks';
import { navigate, routeState } from '../router';
import { ago, clock } from '../time';
import { UploadZone } from '../UploadZone';

const SOURCES: Record<string, string> = { cli: 'pail-cli', upload: 'Upload' };

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
    ({ pail }: { pail: Pail }) => pail.status === 'building',
  );
  const [selected, setSelected] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(() => !!routeState<{ confirm?: boolean }>()?.confirm);
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const [uploading, setUploading] = useState(false);

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

  // act runs one change against the API, then shows where things stand.
  const act = async (change: () => Promise<unknown>) => {
    setNotice('');
    setBusy(true);
    try {
      await change();
    } catch (err) {
      setNotice(message(err));
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
        </section>

        <aside style={{ display: 'flex', flexDirection: 'column', gap: 32, minWidth: 0 }}>
          <section className="pl-stack">
            <h2 className="pl-h2">Addresses</h2>
            <div className="pl-card" style={{ gap: 0, padding: '4px 16px' }}>
              <div className="pl-addr">
                <a className="pl-url" style={{ flexGrow: 1, minWidth: 0, fontSize: 13 }} href={pail.url}>
                  {pail.host}
                </a>
                <span className="pl-tag pl-tag-plain">Default</span>
              </div>
            </div>
          </section>

          <dl className="pl-facts">
            <dt>Source</dt>
            <dd>{SOURCES[pail.source] ?? pail.source}</dd>
            <dt>Serving</dt>
            <dd className="pl-mono" style={{ fontSize: 13 }}>
              {pail.serving || '—'}
            </dd>
          </dl>

          <section className="pl-stack" style={{ gap: 8 }}>
            <h2 className="pl-h2" style={{ fontSize: 16, lineHeight: '22px' }}>
              Same thing, from a terminal
            </h2>
            <Command>{`pail up ./dist --name ${pail.name}`}</Command>
          </section>
        </aside>
      </div>

      <section className="pl-stack" style={{ paddingTop: 24, borderTop: '1px solid var(--line)' }}>
        {confirming ? (
          <div className="pl-note pl-note-failed pl-stack" style={{ padding: 16 }}>
            <p style={{ margin: 0 }}>
              <b>Remove {pail.name}?</b> {pail.host} stops answering, and every deploy is deleted. This can’t be undone.
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
