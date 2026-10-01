import { type FormEvent, useState } from 'react';
import { ApiError, getInfo, type Info, setToken } from '../api';
import { Button, Field } from '../ds';
import { message } from '../hooks';

// Pail has no accounts, but it does have one token per installation. The UI
// asks for it once per browser.
export function TokenStep({ onDone }: { onDone: (info: Info) => void }) {
  const [value, setValue] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!value.trim()) {
      setError("Paste the token. It's the PAIL_TOKEN the server was started with.");
      return;
    }
    setBusy(true);
    setToken(value.trim());
    try {
      onDone(await getInfo());
    } catch (err) {
      setToken(null);
      setError(
        err instanceof ApiError && err.status === 401
          ? "That isn't this Pail's token. Use the one set as PAIL_TOKEN on the server."
          : message(err),
      );
      setBusy(false);
    }
  };

  return (
    <main className="pl-main" style={{ maxWidth: 560, gap: 24 }}>
      <div className="pl-stack" style={{ gap: 8 }}>
        <h1 className="pl-display">This Pail needs its token</h1>
        <p className="pl-muted">
          It's the <code className="pl-mono">PAIL_TOKEN</code> the server was started with. This browser keeps it, so
          you're only asked once.
        </p>
      </div>
      <form onSubmit={submit} className="pl-stack" style={{ gap: 16 }}>
        <Field
          label="Token"
          type="password"
          mono
          autoFocus
          autoComplete="off"
          placeholder="paste it here"
          value={value}
          error={error || undefined}
          onChange={(e) => {
            setValue(e.target.value);
            setError('');
          }}
        />
        <div className="pl-actions">
          <Button variant="primary" type="submit" disabled={busy}>
            Open Pail
          </Button>
        </div>
      </form>
    </main>
  );
}
