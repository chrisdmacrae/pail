import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';
import { type Page, search } from '../../search';

// The one page of the docs that is rendered on demand. On Pail it is a
// function: the adapter builds it, and the build's pail.json routes it.
export const prerender = false;

// GET /api/search?q=custom+domain answers with the pages that fit, best first.
export const GET: APIRoute = async ({ url }) => {
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
  return Response.json(
    { query, results: search(pages, query) },
    // The answer to a query only changes when the docs are deployed.
    { headers: { 'Cache-Control': 'public, max-age=60' } },
  );
};
