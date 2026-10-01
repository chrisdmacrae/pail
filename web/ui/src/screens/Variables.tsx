import { type FormEvent, useCallback, useEffect, useState } from 'react';
import { type Info, listVariables, type Pail, removeVariable, setVariable, type Variable } from '../api';
import { Button, Field } from '../ds';
import { message } from '../hooks';

// A variable's name, as pail.json and the server take it.
const NAME = /^[A-Za-z_][A-Za-z0-9_]*$/;

// Variables lists what pail.json's ${NAME} is filled in with: settings, and
// secrets, which are kept sealed and never shown again.
export function Variables({ pail, info }: { pail: Pail; info: Info | null }) {
  const [variables, setVariables] = useState<Variable[] | null>(null);
  const [name, setName] = useState('');
  const [value, setValue] = useState('');
  const [secret, setSecret] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  // changed says a variable was set or removed since the page opened, which
  // what's running hasn't heard of yet.
  const [changed, setChanged] = useState(false);

  const load = useCallback(() => listVariables(pail.name).then(setVariables, () => {}), [pail.name]);
  useEffect(() => {
    load();
  }, [load]);

  const canSeal = info?.secrets !== false;

  const change = async (work: () => Promise<unknown>) => {
    setError('');
    setBusy(true);
    try {
      await work();
      setChanged(true);
      await load();
    } catch (err) {
      setError(message(err));
    }
    setBusy(false);
  };

  const add = (e: FormEvent) => {
    e.preventDefault();
    const key = name.trim();
    if (!key) return;
    if (!NAME.test(key)) {
      setError('A name is letters, numbers and underscores, and doesn’t start with a number, like DATABASE_PASSWORD.');
      return;
    }
    change(async () => {
      await setVariable(pail.name, key, value, secret && canSeal);
      setName('');
      setValue('');
    });
  };

  return (
    <section className="pl-stack">
      <h2 className="pl-h2">Variables</h2>
      <p className="pl-small" style={{ margin: 0 }}>
        Settings and secrets kept here instead of in the repo. <code className="pl-mono">pail.json</code> uses one in a
        container’s or a function’s <code className="pl-mono">env</code> as{' '}
        <code className="pl-mono">$&#123;NAME&#125;</code>.
      </p>

      {variables !== null && variables.length > 0 && (
        <div className="pl-card" style={{ gap: 0, padding: '4px 16px' }}>
          {variables.map((v) => (
            <div className="pl-addr" key={v.name}>
              <span className="pl-mono pl-var-name">{v.name}</span>
              {v.secret ? (
                <span className="pl-tag pl-tag-plain">Secret</span>
              ) : (
                <span className="pl-mono pl-var-value" title={v.value}>
                  {v.value || '(empty)'}
                </span>
              )}
              <Button
                size="sm"
                variant="ghost"
                icon="trash"
                aria-label={`Remove ${v.name}`}
                title="Remove variable"
                disabled={busy}
                onClick={() => change(() => removeVariable(pail.name, v.name))}
              />
            </div>
          ))}
        </div>
      )}

      {changed && (
        <p className="pl-small" role="status" style={{ margin: 0 }}>
          What’s running keeps what it started with. Redeploy to use the change.
        </p>
      )}

      <form onSubmit={add} className="pl-stack" style={{ gap: 8 }}>
        <Field
          label="Name"
          mono
          placeholder="DATABASE_PASSWORD"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          value={name}
          error={error || undefined}
          onChange={(e) => {
            setName(e.target.value);
            setError('');
          }}
        />
        <Field
          label="Value"
          mono
          type={secret ? 'password' : 'text'}
          autoComplete="off"
          spellCheck={false}
          value={value}
          hint={variables?.some((v) => v.name === name.trim()) ? 'This replaces the one it has now.' : undefined}
          onChange={(e) => setValue(e.target.value)}
        />
        <label className="pl-check">
          <input
            type="checkbox"
            checked={secret && canSeal}
            disabled={!canSeal}
            onChange={(e) => setSecret(e.target.checked)}
          />
          Keep it secret
        </label>
        <p className="pl-small" style={{ margin: 0 }}>
          {canSeal ? (
            'A secret is stored sealed and never shown again, here or by pail-cli. To change one, set it again.'
          ) : (
            <>
              This Pail has no key to seal secrets with. Set <code className="pl-mono">PAIL_SECRETS_KEY</code> on the
              server, or give it a data folder it can write.
            </>
          )}
        </p>
        <div>
          <Button type="submit" disabled={busy || !name.trim()}>
            Set variable
          </Button>
        </div>
      </form>
    </section>
  );
}
