import { useSyncExternalStore } from 'react';

// A route is a path plus whatever the last navigation handed over.
function subscribe(notify: () => void): () => void {
  window.addEventListener('popstate', notify);
  return () => window.removeEventListener('popstate', notify);
}

export function usePath(): string {
  return useSyncExternalStore(subscribe, () => window.location.pathname);
}

export function navigate(path: string, state?: unknown): void {
  window.history.pushState(state ?? null, '', path);
  window.dispatchEvent(new PopStateEvent('popstate'));
  window.scrollTo(0, 0);
}

// routeState is what the navigation that brought us here handed over.
export function routeState<T>(): T | null {
  return (window.history.state as T | null) ?? null;
}

export const pailPath = (name: string) => `/pails/${encodeURIComponent(name)}`;
