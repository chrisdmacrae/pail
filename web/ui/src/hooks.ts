import { useEffect, useRef, useState } from 'react';
import { ApiError } from './api';

const BUSY_MS = 1500;
const IDLE_MS = 10_000;

export interface Polled<T> {
  data?: T;
  error?: ApiError;
  // refresh loads again now, after something was changed.
  refresh: () => void;
}

// usePoll keeps data fresh: quickly while busy(data) says something is
// building, slowly otherwise, and starting over when key changes.
export function usePoll<T>(key: string, load: () => Promise<T>, busy: (data: T) => boolean): Polled<T> {
  const [state, setState] = useState<{ key: string; data?: T; error?: ApiError }>({ key });
  const latest = useRef({ load, busy });
  latest.current = { load, busy };
  // Loads again now, restarting the wait for the next turn.
  const reload = useRef(() => {});

  useEffect(() => {
    let live = true;
    let timer = 0;
    const run = async () => {
      window.clearTimeout(timer);
      let wait = IDLE_MS;
      try {
        const data = await latest.current.load();
        if (!live) return;
        setState({ key, data });
        if (latest.current.busy(data)) wait = BUSY_MS;
      } catch (err) {
        if (!live) return;
        const error = err instanceof ApiError ? err : new ApiError(0, 'error', String(err));
        setState((prev) => ({ key, data: prev.key === key ? prev.data : undefined, error }));
      }
      window.clearTimeout(timer);
      timer = window.setTimeout(run, wait);
    };
    reload.current = run;
    run();
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [key]);

  // What's on screen belongs to the key that was asked for, never the last one.
  const current = state.key === key ? state : { key };
  return { data: current.data, error: current.error, refresh: () => reload.current() };
}

// message is what to show a person for anything thrown.
export function message(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
