// Search over the docs collection. Every request starts afresh, so there is
// no index kept: the pages are few, and reading them all is quick.

/** A page of the docs, as much of it as search reads. */
export interface Page {
  /** The page's path, without slashes at either end: deploying/functions. */
  id: string;
  title: string;
  lead: string;
  section: string;
  /** The page's Markdown. */
  body: string;
  /** Its headings in order, with the ids they have on the page. */
  headings: { depth: number; slug: string; text: string }[];
}

/** One result: a page, or the part of one that fits best. */
export interface Hit {
  title: string;
  section: string;
  /** The heading the link goes to, when it goes to part of a page. */
  heading?: string;
  href: string;
  excerpt: string;
}

const EXCERPT = 160;

/**
 * The pages that have every word of the query, best first. A word matches
 * where a word of the page starts with it, so "deploy" finds "deploying".
 */
export function search(pages: Page[], query: string, limit = 8): Hit[] {
  const terms = words(query).slice(0, 8);
  if (terms.length === 0) return [];
  const tests = terms.map((term) => new RegExp(`(?<![\\p{L}\\p{N}])${escape(term)}`, 'giu'));
  const count = (test: RegExp, text: string) => text.match(test)?.length ?? 0;

  const hits: { score: number; hit: Hit }[] = [];
  for (const page of pages) {
    const parts = split(page);
    let score = 0;
    let all = true;
    const partScores = parts.map(() => 0);
    for (const test of tests) {
      const inTitle = count(test, page.title) > 0;
      const inLead = count(test, page.lead) > 0;
      let found = inTitle || inLead;
      parts.forEach((part, i) => {
        const here = (count(test, part.heading ?? '') > 0 ? 5 : 0) + Math.min(count(test, part.text), 5);
        partScores[i] += here;
        found ||= here > 0;
      });
      if (!found) {
        all = false;
        break;
      }
      score += (inTitle ? 12 : 0) + (inLead ? 4 : 0);
    }
    if (!all) continue;

    // The part that says most about the query is where the link goes, unless
    // the page as a whole is the better answer: its title has every word.
    const best = partScores.indexOf(Math.max(...partScores));
    const part = parts[best];
    const whole = tests.every((test) => count(test, page.title) > 0) || partScores[best] === 0;
    score += partScores.reduce((sum, s) => sum + s, 0);

    const anchor = !whole && part.slug ? `#${part.slug}` : '';
    hits.push({
      score,
      hit: {
        title: page.title,
        section: page.section,
        heading: anchor ? part.heading : undefined,
        href: `/${page.id}/${anchor}`,
        excerpt: whole ? page.lead : excerpt(part.text, tests),
      },
    });
  }
  // A stable sort: pages that score the same stay in the order they came in.
  return hits
    .sort((a, b) => b.score - a.score)
    .slice(0, limit)
    .map(({ hit }) => hit);
}

/** The words of a query, lower-cased, each once. */
export function words(query: string): string[] {
  return [...new Set(query.toLowerCase().match(/[\p{L}\p{N}]+/gu) ?? [])];
}

const escape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

interface Part {
  heading?: string;
  slug?: string;
  text: string;
}

/** A page as its parts: what comes before the first heading, then each heading's. */
function split(page: Page): Part[] {
  const headings = page.headings.filter((h) => h.depth > 1);
  const parts: (Part & { lines: string[] })[] = [{ text: '', lines: [] }];
  let fenced = false;
  let next = 0;
  for (const line of page.body.split('\n')) {
    if (/^\s*(```|~~~)/.test(line)) {
      fenced = !fenced;
      continue;
    }
    // A line that starts with # inside a code block is a comment, not a heading.
    const heading = fenced ? null : /^#{2,6}\s+(.*)$/.exec(line);
    if (heading) {
      const known = headings[next++];
      parts.push({ heading: known?.text ?? plain(heading[1]), slug: known?.slug, text: '', lines: [] });
    } else {
      parts[parts.length - 1].lines.push(line);
    }
  }
  return parts.map(({ lines, ...part }) => ({ ...part, text: plain(lines.join('\n')) }));
}

/** Markdown as the words a reader sees. */
function plain(markdown: string): string {
  return markdown
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/<[^>]+>/g, ' ')
    .replace(/^\s*(?:[-*+]|\d+\.|>)\s+/gm, '')
    .replace(/^\s*\|?[\s:|-]+\|?\s*$/gm, ' ')
    .replace(/[*_`|]/g, ' ')
    .replace(/\s+/g, ' ')
    .trim();
}

/** The stretch of a text around the first word of the query it has. */
function excerpt(text: string, tests: RegExp[]): string {
  let first = -1;
  for (const test of tests) {
    const at = text.search(new RegExp(test.source, 'iu'));
    if (at >= 0 && (first < 0 || at < first)) first = at;
  }
  if (text.length <= EXCERPT) return text;
  let start = Math.max(0, first - 40);
  // Start on a whole word.
  if (start > 0) start = text.indexOf(' ', start) + 1;
  const end = Math.min(text.length, start + EXCERPT);
  const cut = end < text.length ? text.lastIndexOf(' ', end) : end;
  return `${start > 0 ? '…' : ''}${text.slice(start, cut > start ? cut : end)}${end < text.length ? '…' : ''}`;
}
