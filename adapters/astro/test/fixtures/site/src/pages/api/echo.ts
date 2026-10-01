import type { APIRoute } from 'astro';

export const prerender = false;

export const GET: APIRoute = ({ url, request }) =>
  Response.json({ q: url.searchParams.get('q'), agent: request.headers.get('user-agent'), secret: process.env.SECRET ?? null });

export const POST: APIRoute = async ({ request }) => {
  const body = await request.arrayBuffer();
  return new Response(body, { status: 201, headers: { 'Content-Type': request.headers.get('content-type') ?? 'application/octet-stream', 'X-Bytes': String(body.byteLength) } });
};
