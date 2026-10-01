import { type FormEvent, useCallback, useEffect, useState } from 'react';
import { addHost, type Host, type Info, listHosts, type Pail, removeHost } from '../api';
import { Button, Field } from '../ds';
import { message } from '../hooks';

// How often to look again while a hostname's DNS hasn't caught up.
const RECHECK_MS = 15_000;

// Addresses lists where a pail answers: its own name, then any custom
// hostnames, each with whether its DNS reaches this Pail yet.
export function Addresses({ pail, info }: { pail: Pail; info: Info | null }) {
  const [checked, setChecked] = useState<Host[] | null>(null);
  const [draft, setDraft] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => listHosts(pail.name).then(setChecked, () => {}), [pail.name]);
  useEffect(() => {
    load();
  }, [load]);

  const waiting = checked?.some((h) => !h.points_here) ?? false;
  useEffect(() => {
    if (!waiting) return;
    const timer = window.setInterval(load, RECHECK_MS);
    return () => window.clearInterval(timer);
  }, [waiting, load]);

  // Until the first check comes back, show the names without a verdict.
  const hosts: Partial<Host>[] = checked ?? [
    { host: pail.host, url: pail.url, default: true },
    ...pail.hosts.map((host) => ({ host, default: false })),
  ];

  const change = async (work: () => Promise<unknown>) => {
    setError('');
    setBusy(true);
    try {
      await work();
      await load();
    } catch (err) {
      setError(message(err));
    }
    setBusy(false);
  };

  const add = (e: FormEvent) => {
    e.preventDefault();
    if (!draft.trim()) return;
    change(async () => {
      await addHost(pail.name, draft);
      setDraft('');
    });
  };

  return (
    <section className="pl-stack">
      <h2 className="pl-h2">Addresses</h2>
      <div className="pl-card" style={{ gap: 0, padding: '4px 16px' }}>
        {hosts.map((h) => (
          <div className="pl-addr" key={h.host}>
            <a className="pl-url" href={h.url ?? `http://${h.host}`}>
              {h.host}
            </a>
            {h.default && <span className="pl-tag pl-tag-plain">Default</span>}
            {h.points_here === true && !h.default && <span className="pl-tag pl-tag-serving">Points here</span>}
            {h.points_here === false && (
              <span className="pl-tag pl-tag-plain" title={h.detail}>
                Not pointing here yet
              </span>
            )}
            {!h.default && h.host && (
              <Button
                size="sm"
                variant="ghost"
                icon="trash"
                aria-label={`Remove ${h.host}`}
                title="Remove hostname"
                disabled={busy}
                onClick={() => change(() => removeHost(pail.name, h.host as string))}
              />
            )}
          </div>
        ))}
      </div>

      {info?.custom_hostnames === true && (
        <>
          <form onSubmit={add} style={{ display: 'flex', gap: 8, alignItems: 'flex-start' }}>
            <div style={{ flex: '1 1 auto', minWidth: 0 }}>
              <Field
                label="Add a hostname"
                mono
                placeholder="recipes.home.example"
                value={draft}
                error={error || undefined}
                onChange={(e) => {
                  setDraft(e.target.value);
                  setError('');
                }}
              />
            </div>
            {/* Sits level with the input, below the label. */}
            <Button type="submit" disabled={busy} style={{ marginTop: 22 }}>
              Add
            </Button>
          </form>
          {busy && info.tls === 'acme' && draft && (
            <p className="pl-small" role="status">
              Getting {draft.trim()} a certificate. This waits for DNS and can take a minute or two.
            </p>
          )}
          <p className="pl-small">
            {info.tls === 'off'
              ? 'This Pail serves plain HTTP, so the proxy in front of it has to answer for the hostname and pass it on.'
              : `Point it at this Pail server: a CNAME to ${info.base_domain}, or an A record to this server’s address.`}
          </p>
        </>
      )}
      {info?.custom_hostnames === false && (
        <>
          {error && (
            <div className="pl-note pl-note-failed" role="alert">
              {error}
            </div>
          )}
          <p className="pl-small">
            Custom hostnames need a domain you own and a DNS token, set with{' '}
            <code className="pl-mono">PAIL_ACME_DNS_PROVIDER</code> and{' '}
            <code className="pl-mono">PAIL_ACME_DNS_TOKEN</code>, or a proxy in front of Pail that handles HTTPS, with{' '}
            <code className="pl-mono">PAIL_TLS=off</code>.
          </p>
        </>
      )}
    </section>
  );
}
