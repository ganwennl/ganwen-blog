(() => {
  'use strict';
  let mode = 'system';
  try {
    const saved = localStorage.getItem('ganwen.theme.v1');
    if (['light', 'dark', 'system'].includes(saved)) mode = saved;
  } catch {}
  const theme = mode === 'system' ? (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light') : mode;
  document.documentElement.dataset.theme = theme;
  document.documentElement.dataset.themeMode = mode;
  document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#1e1e1e' : '#eff4f9');
})();
