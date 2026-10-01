import type { APIRoute } from 'astro';

export const prerender = false;

export const GET: APIRoute = () => {
  throw new Error('this route is broken');
};
