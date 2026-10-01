import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import './ds';
import './app.css';
import { App } from './App';

// Day or Night follows the device; Pail has no settings to choose one in.
const night = window.matchMedia('(prefers-color-scheme: dark)');
const applyTheme = () => {
  document.documentElement.dataset.theme = night.matches ? 'dark' : 'light';
};
applyTheme();
night.addEventListener('change', applyTheme);

const root = document.getElementById('root');
if (!root) throw new Error('index.html has no #root to render Pail into');

createRoot(root).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
