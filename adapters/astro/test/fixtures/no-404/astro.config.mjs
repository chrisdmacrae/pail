import { defineConfig } from 'astro/config';
import pail from '../../../src/index.js';

export default defineConfig({ adapter: pail() });
