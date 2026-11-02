// Stryker disable all: DOM mount bootstrap, no test reaches it
import { mount } from 'svelte'
import './app.css'
import App from './App.svelte'

const app = mount(App, {
  target: document.getElementById('app')!,
})

// The worker only in a production build: the dev server must stay uncached.
if (import.meta.env.PROD && 'serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(() => {
    // The offline shell is a nicety; the app works without it.
  })
}

export default app
