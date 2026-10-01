import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';
import { type Page, search } from '../../search';

// The one page of the docs that is rendered on demand. On Pail it is a
// function: the adapter builds it, and the build's pail.json routes it.
export const prerender = false;

// GET /api/search?q=custom+domain answers with the pages that fit, best first.
export const GET: APIRoute = async ({ url }) => {
  const began = performance.now();
  const query = (url.searchParams.get('q') ?? '').trim().slice(0, 100);
  const entries = (await getCollection('docs')).sort((a, b) => a.data.order - b.data.order);
  const pages: Page[] = entries.map((entry) => ({
    id: entry.id,
    title: entry.data.title,
    lead: entry.data.lead,
    section: entry.data.section,
    body: entry.body ?? '',
    headings: (entry.rendered?.metadata?.headings as Page['headings'] | undefined) ?? [],
  }));
  const results = search(pages, query);

  // One line for each search, in the pail's output: pail logs <pail> --output.
  // The query is quoted, so that whatever was typed stays on its line.
  const took = Math.round(performance.now() - began);
  const found = `${results.length} ${results.length === 1 ? 'result' : 'results'}`;
  console.log(`search ${JSON.stringify(query)}: ${found} in ${took}ms`);

  return Response.json(
    { query, results },
    // The answer to a query only changes when the docs are deployed.
    { headers: { 'Cache-Control': 'public, max-age=60' } },
  );
};
