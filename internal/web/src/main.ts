import { mount } from 'svelte';
import '@fontsource-variable/geist'; // UI/prose sans (--font-sans), self-hosted
import '@fontsource-variable/geist-mono'; // code/identifier mono (--font-mono)
import './app.css';
import App from './App.svelte';

const target = document.getElementById('app');
if (!target) throw new Error('#app mount point missing');

export default mount(App, { target });
