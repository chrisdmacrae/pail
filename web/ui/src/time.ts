// ago says how long ago something happened, the way the pail list does.
export function ago(when: string | Date, now: Date = new Date()): string {
  const minutes = Math.floor((now.getTime() - new Date(when).getTime()) / 60_000);
  const hours = Math.floor(minutes / 60);
  const days = Math.floor(hours / 24);
  if (minutes < 1) return 'just now';
  if (minutes < 60) return `${minutes} min ago`;
  if (hours < 24) return `${hours} hr ago`;
  if (hours < 48) return 'yesterday';
  if (days < 14) return `${days} days ago`;
  return `${Math.floor(days / 7)} weeks ago`;
}

// clock is a log line's time of day, in the viewer's own timezone.
export function clock(when: string): string {
  return new Date(when).toLocaleTimeString([], { hour12: false });
}

const NUMBERS = ['', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine', 'ten'];

// kept says how many good deploys this Pail keeps to roll back to, which the
// server sets with PAIL_MAX_DEPLOYS.
export function kept(max: number | undefined): string {
  if (!max) return 'the latest stay';
  if (max === 1) return 'the last one stays';
  return `the last ${NUMBERS[max] ?? max} stay`;
}
