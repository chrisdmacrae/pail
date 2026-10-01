import bricolage700 from '@fontsource/bricolage-grotesque/files/bricolage-grotesque-latin-700-normal.woff?inline';
import hanken400 from '@fontsource/hanken-grotesk/files/hanken-grotesk-latin-400-normal.woff?inline';
import plexMono500 from '@fontsource/ibm-plex-mono/files/ibm-plex-mono-latin-500-normal.woff?inline';
import type { APIRoute } from 'astro';
import { experimental_AstroContainer as AstroContainer } from 'astro/container';
import satori from 'satori';
import { html } from 'satori-html';
import sharp from 'sharp';
import OgImage from '../../components/OgImage.astro';

// Rendered on demand, by the same function that answers the search. A page
// names its picture in its og:image tag, and the picture is drawn the first
// time something asks for it.
export const prerender = false;

const WIDTH = 1200;
const HEIGHT = 630;

// Satori can't read woff2, which is all the design system keeps, so the same
// three faces come from Fontsource as woff. They are built into the function:
// it can't ask its own pail for a file.
const font = (dataURI: string) => Buffer.from(dataURI.slice(dataURI.indexOf(',') + 1), 'base64');
const fonts = [
  { name: 'Bricolage Grotesque', data: font(bricolage700), weight: 700, style: 'normal' },
  { name: 'Hanken Grotesk', data: font(hanken400), weight: 400, style: 'normal' },
  { name: 'IBM Plex Mono', data: font(plexMono500), weight: 500, style: 'normal' },
] as const;

// Astro escapes what a component is given. Satori draws text as it finds it,
// so the escapes are turned back once the markup has been read.
const ESCAPES: Record<string, string> = { '&amp;': '&', '&lt;': '<', '&gt;': '>', '&quot;': '"', '&#39;': "'" };
function unescape(node: unknown): unknown {
  if (typeof node === 'string') return node.replace(/&(?:amp|lt|gt|quot|#39);/g, (escape) => ESCAPES[escape]);
  if (Array.isArray(node)) return node.map(unescape);
  if (node && typeof node === 'object' && 'props' in node) {
    const { children, ...props } = node.props as Record<string, unknown>;
    return { ...node, props: { ...props, children: unescape(children) } };
  }
  return node;
}

// GET /api/og.png?title=Quickstart answers with the picture for that title.
// lead and section are drawn too, when given.
export const GET: APIRoute = async ({ url }) => {
  const param = (name: string, most: number) => (url.searchParams.get(name) ?? '').trim().slice(0, most);
  const title = param('title', 80);
  if (!title) return new Response('Give a title: /api/og.png?title=Quickstart', { status: 400 });

  // Component to markup, markup to SVG, SVG to PNG.
  const container = await AstroContainer.create();
  const markup = await container.renderToString(OgImage, {
    props: { title, lead: param('lead', 200), section: param('section', 40) },
  });
  const svg = await satori(unescape(html(markup)) as Parameters<typeof satori>[0], {
    width: WIDTH,
    height: HEIGHT,
    fonts: [...fonts],
  });
  const png = await sharp(Buffer.from(svg)).png().toBuffer();

  return new Response(new Uint8Array(png), {
    headers: {
      'Content-Type': 'image/png',
      // The same title always draws the same picture.
      'Cache-Control': 'public, max-age=31536000, immutable',
    },
  });
};
