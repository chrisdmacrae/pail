import { defineCollection } from 'astro:content';
import { glob } from 'astro/loaders';
import { z } from 'astro/zod';

// Every page of the docs is a Markdown file in src/content/docs. Its path is
// its URL: custom-domains/cloudflare.md is /custom-domains/cloudflare/.
const docs = defineCollection({
  loader: glob({ pattern: '**/*.md', base: './src/content/docs' }),
  schema: z.object({
    title: z.string(),
    // lead is the sentence or two under the title; also the page's description.
    lead: z.string(),
    // section is the sidebar group the page sits in.
    section: z.enum(['Start here', 'Custom domains']),
    // order sorts pages inside their section.
    order: z.number(),
    // navLabel is a shorter name for the sidebar, when the title is long.
    navLabel: z.string().optional(),
    // provider marks a DNS provider's guide and holds the value
    // PAIL_ACME_DNS_PROVIDER takes for it. These pages nest under their
    // section's first page in the sidebar.
    provider: z.string().optional(),
    // providerTiles lists every provider guide as tiles under the lead.
    providerTiles: z.boolean().default(false),
    // next points at the page to read after this one.
    next: z.object({ href: z.string(), label: z.string() }).optional(),
  }),
});

export const collections = { docs };
