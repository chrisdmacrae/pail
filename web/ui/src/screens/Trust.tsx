import type { Info } from '../api';
import { navigate } from '../router';

// What each kind of device needs done, once, to trust this Pail's root.
const PLATFORMS: [string, string][] = [
  ['macOS', 'Open ca.crt, add it to the System keychain, and set it to Always Trust.'],
  ['Windows', 'Import it into Trusted Root Certification Authorities.'],
  [
    'iOS and iPadOS',
    'Install the profile, then turn on full trust under Settings → General → About → Certificate Trust Settings.',
  ],
  ['Android', 'Install it as a user CA certificate. Chrome honours it; most other apps ignore user CAs.'],
  [
    'Linux',
    'Copy it to the system CA folder and run update-ca-certificates. Firefox and Chrome may need it imported into their own store.',
  ],
  ['Firefox, anywhere', 'It uses its own store unless enterprise roots are turned on; import it there.'],
];

// Trust explains how a device comes to trust an installation that signs its
// own certificates. A Pail on Let's Encrypt, or on plain HTTP, has nothing
// to hand out.
export function Trust({ host, info }: { host: string; info: Info | null }) {
  const own = info?.tls === 'internal';
  return (
    <main className="pl-main pl-main-narrow">
      <div className="pl-stack" style={{ gap: 8 }}>
        <button type="button" className="pl-back" onClick={() => navigate('/')}>
          ← All pails
        </button>
        <h1 className="pl-display">Trust this Pail</h1>
      </div>

      {info && !own && (
        <p className="pl-muted">
          {info.tls === 'acme'
            ? 'This Pail’s certificates come from Let’s Encrypt, which your devices already trust. There’s nothing to install.'
            : 'This Pail serves plain HTTP, so there’s no certificate to trust.'}
        </p>
      )}

      {own && (
        <>
          <section className="pl-stack">
            <p className="pl-muted">
              This Pail signs its own certificates for {host} and every pail under it. Each device trusts its root
              certificate once; after that, pails open without a warning. The root can only vouch for names under {host}
              , so trusting it can’t be used to impersonate any other site.
            </p>
            <div className="pl-actions">
              <a className="pa-btn pa-btn-primary pl-link-btn" href="/ca.crt" download={`pail-${host}.crt`}>
                Download ca.crt
              </a>
            </div>
            <p className="pl-small">
              It’s also at <code className="pl-mono">http://{host}/ca.crt</code>, for a device that can’t open this page
              yet. From a terminal, <code className="pl-mono">pail ca</code> prints it.
            </p>
          </section>

          <section className="pl-stack">
            <h2 className="pl-h2">On each device</h2>
            <dl className="pl-facts" style={{ gridTemplateColumns: 'minmax(120px, auto) minmax(0, 1fr)' }}>
              {PLATFORMS.map(([name, steps]) => (
                <div key={name} style={{ display: 'contents' }}>
                  <dt style={{ color: 'var(--ink)', fontWeight: 600 }}>{name}</dt>
                  <dd>{steps}</dd>
                </div>
              ))}
            </dl>
          </section>
        </>
      )}
    </main>
  );
}
