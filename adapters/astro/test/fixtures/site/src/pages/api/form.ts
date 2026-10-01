import type { APIRoute } from 'astro';

export const prerender = false;

export const POST: APIRoute = async ({ request, redirect }) => {
  const form = await request.formData();
  return redirect(`/blog/${form.get('slug')}`, 303);
};
