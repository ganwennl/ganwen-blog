(() => {
  'use strict';
  const themeKey = 'ganwen.theme.v1';
  const systemTheme = matchMedia('(prefers-color-scheme: dark)');
  let themeMode = 'system';
  try {
    const saved = localStorage.getItem(themeKey);
    if (['light', 'dark', 'system'].includes(saved)) themeMode = saved;
  } catch {}
  function applyTheme() {
    const theme = themeMode === 'system' ? (systemTheme.matches ? 'dark' : 'light') : themeMode;
    document.documentElement.dataset.theme = theme;
    document.documentElement.dataset.themeMode = themeMode;
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', theme === 'dark' ? '#1e1e1e' : '#eff4f9');
    for (const button of document.querySelectorAll('[data-theme-choice]')) {
      button.setAttribute('aria-pressed', String(button.dataset.themeChoice === themeMode));
    }
  }
  applyTheme();
  systemTheme.addEventListener('change', () => {
    if (themeMode === 'system') applyTheme();
  });
  addEventListener('storage', event => {
    if (event.key !== themeKey && event.key !== null) return;
    themeMode = ['light', 'dark', 'system'].includes(event.newValue) ? event.newValue : 'system';
    applyTheme();
  });
  function supportedLanguages() {
    const configured = (document.documentElement.dataset.supportedLanguages || '').split(/\s+/);
    const linked = Array.from(document.querySelectorAll('.language-links a[lang]'), link => link.getAttribute('lang'));
    return new Set(['zh', ...configured, ...linked].filter(language => /^[a-z]{2}$/.test(language)));
  }
  function isPublicPath(pathname) {
    if (pathname === '/' || pathname === '/index.php') return true;
    const match = /^\/([a-z]{2})(?:\/(?:index\.php)?)?$/.exec(pathname);
    return Boolean(match && match[1] !== 'zh' && supportedLanguages().has(match[1]));
  }
  function analyticsPath(value) {
    try {
      const url = new URL(value, location.origin);
      if (url.origin !== location.origin || !isPublicPath(url.pathname)) return null;
      if (['attachment_id', 'preview', 'preview_id', 'feed', 'sitemap', 'robots'].some(key => url.searchParams.has(key))) return null;
      if (url.searchParams.has('p') && url.searchParams.has('page_id')) return null;
      const query = new URLSearchParams();
      for (const key of ['p', 'page_id', 'cat', 'paged', 'author']) {
        const values = url.searchParams.getAll(key);
        if (!values.length) continue;
        if (values.length !== 1 || !/^[1-9]\d{0,15}$/.test(values[0])) return null;
        query.set(key, values[0]);
      }
      for (const key of ['category_name', 'tag', 'post_format']) {
        const values = url.searchParams.getAll(key);
        if (!values.length) continue;
        if (values.length !== 1 || !/^[\p{L}\p{N}_ ,.-]{1,128}$/u.test(values[0])) return null;
        if (key === 'post_format' && !['aside', 'video', 'standard', 'image', 'quote', 'link', 'audio', 'gallery', 'status', 'chat'].includes(values[0])) return null;
        query.set(key, values[0]);
      }
      if (url.searchParams.has('m')) {
        const month = url.searchParams.getAll('m');
        if (month.length !== 1 || !/^\d{4}(?:0[1-9]|1[0-2])$/.test(month[0])) return null;
        query.set('m', month[0]);
      }
      // Keep the search page category, never transmit the visitor's words.
      if (url.searchParams.has('s')) query.set('s', '');
      const serialized = query.toString();
      return url.pathname + (serialized ? '?' + serialized : '');
    } catch { return null; }
  }
  function randomIdentifier() {
    const random = globalThis.crypto;
    if (typeof random?.randomUUID === 'function') return random.randomUUID();
    if (typeof random?.getRandomValues !== 'function') return null;
    return Array.from(random.getRandomValues(new Uint8Array(16)), value => value.toString(16).padStart(2, '0')).join('');
  }
  function anonymousIdentifiers() {
    const readID = key => {
      let value;
      try { value = sessionStorage.getItem(key); } catch {}
      if (!/^[a-f0-9-]{32,36}$/i.test(value || '')) {
        value = randomIdentifier();
        if (!value) return null;
        try { sessionStorage.setItem(key, value); } catch {}
      }
      return value;
    };
    const visitor = readID('ganwen.analytics.visitor.v1');
    let session = readID('ganwen.analytics.session.v1');
    const now = Date.now();
    try {
      const lastSeen = Number(sessionStorage.getItem('ganwen.analytics.last-seen.v1'));
      if (!lastSeen || now - lastSeen > 1800000) {
        session = randomIdentifier();
        if (session) sessionStorage.setItem('ganwen.analytics.session.v1', session);
      }
      sessionStorage.setItem('ganwen.analytics.last-seen.v1', String(now));
    } catch {}
    return visitor && session ? {visitor_id: visitor, session_id: session} : null;
  }
  function initializePublicSession(archiveURL) {
    const main = document.querySelector('[data-public-language]');
    const buttons = Array.from(document.querySelectorAll('[data-article-edit]'));
    const dialog = document.querySelector('[data-public-editor]');
    const frame = document.querySelector('[data-public-editor-frame]');
    const closeButton = document.querySelector('[data-article-editor-close]');
    const postID = button => Number(button.dataset.editPostId || main?.dataset.postId);
    const editable = buttons.filter(button => Number.isSafeInteger(postID(button)) && postID(button) > 0 && dialog && frame);
    const editableButtons = new Set(editable);
    let activeID = 0;
    let activeButton = null;
    let activeSession = '';
    let editorReady = false;
    let legacyEditor = false;
    let editorChanged = false;
    let editorPublic = true;
    let editorVisibilityKnown = false;
    let editorPublicTarget = null;
    let editorGeneration = 0;
    let restoreFocusID = 0;
    let pendingRefresh = null;
    let refreshController = null;
    let authentication = null;
    let sent = false;
    const positionKey = 'ganwen.editor-position.v1';
    const publicLanguage = supportedLanguages().has(main?.dataset.publicLanguage) ? main.dataset.publicLanguage : 'zh';
    const safeEditorBackLink = () => {
      const fallback = publicLanguage === 'zh' ? '/' : '/' + publicLanguage + '/';
      const stored = archiveURL(document.querySelector('[data-back-link]')?.getAttribute('href'));
      if (!stored) return fallback;
      const storedLanguage = /^\/([a-z]{2})(?:\/|$)/.exec(new URL(stored, location.origin).pathname)?.[1] || 'zh';
      return storedLanguage === publicLanguage ? stored : fallback;
    };
    const restoreEditorFocus = () => {
      if (authentication !== true || !restoreFocusID) return;
      editable.find(button => postID(button) === restoreFocusID)?.focus({preventScroll: true});
      restoreFocusID = 0;
    };
    const isOpen = () => Boolean(dialog?.open);
    const detectLegacyEditor = () => {
      if (!isOpen() || editorReady) return;
      try {
        const url = new URL(frame.contentWindow.location.href);
        const doc = frame.contentDocument;
        if (url.origin !== location.origin || url.pathname !== '/admin/' || url.searchParams.get('embed') !== '1'
            || Number(url.searchParams.get('edit')) !== activeID || url.searchParams.get('editor_session') !== activeSession
            || !doc || doc.readyState === 'loading' || doc.documentElement.getAttribute('data-editor-protocol') !== null) return;
        if (['login', 'shell', 'savePostButton'].every(id => doc.getElementById(id))
            && typeof doc.getElementById('savePostButton').onclick === 'function') {
          legacyEditor = true;
          editorReady = true;
        }
      } catch {}
    };
    const refreshSavedPage = async (record, generation) => {
      let target = record.target;
      const isCurrent = () => generation === editorGeneration && !isOpen() && pendingRefresh === record && location.href === record.sourceURL;
      const applyRefresh = () => {
        if (!isCurrent()) return;
        pendingRefresh = null;
        const current = location.pathname + location.search + location.hash;
        if (target === current) {
          try {sessionStorage.setItem(positionKey, JSON.stringify({url: target, scroll: record.scroll,
            savedAt: Date.now(), postId: record.postId}));} catch {}
          location.reload();
        } else location.assign(target);
      };
      const guardedArchive = value => {
        const archive = archiveURL(value), url = archive && new URL(archive, location.origin);
        const page = Number(url?.searchParams.get('paged'));
        const paged = Number.isSafeInteger(page) && page > 1;
        const filtered = Boolean(url && ['cat', 'category_name', 'tag', 'author'].some(key => url.searchParams.get(key)?.trim()));
        return url && (paged || filtered) ? {url, paged, filtered} : null;
      };
      let archive = guardedArchive(target);
      if (!archive && !record.checkArticle) {applyRefresh(); return;}
      const controller = new AbortController();
      refreshController = controller;
      const timeout = setTimeout(() => controller.abort(), 5000);
      try {
        const options = {method: 'HEAD', credentials: 'same-origin', cache: 'no-store', signal: controller.signal};
        let response = await fetch(target, options);
        if (!isCurrent()) return;
        if (response.status === 404 && record.checkArticle) {
          target = record.backTarget;
          archive = guardedArchive(target);
          if (archive) {
            response = await fetch(target, options);
            if (!isCurrent()) return;
          }
        }
        if (response.status === 404 && archive?.paged) {
          archive.url.searchParams.set('paged', '1'); target = archive.url.pathname + archive.url.search + archive.url.hash;
          if (archive.filtered) {
            response = await fetch(target, options);
            if (!isCurrent()) return;
          }
        }
        if (response.status === 404 && archive?.filtered) {
          target = (publicLanguage === 'zh' ? '/' : '/' + publicLanguage + '/') + '?paged=1';
        }
      } catch {} finally {
        clearTimeout(timeout);
        if (refreshController === controller) refreshController = null;
      }
      applyRefresh();
    };
    const finishEditorClose = () => {
      if (!activeID) return;
      const currentArticle = document.querySelector('.article-card') && Number(main?.dataset.postId) === activeID;
      const leaveHiddenArticle = editorChanged && !editorPublic && currentArticle;
      document.body.classList.remove('has-article-editor');
      frame.removeAttribute('src');
      activeButton?.focus({preventScroll: true});
      if (editorChanged || pendingRefresh) {
        const returnToList = editorChanged ? Boolean(leaveHiddenArticle) : pendingRefresh.returnToList;
        pendingRefresh = {sourceURL: location.href, target: returnToList ? editorChanged ? safeEditorBackLink() : pendingRefresh.target
          : editorChanged && editorPublicTarget ? editorPublicTarget : location.pathname + location.search + location.hash, returnToList, scroll: scrollY, postId: activeID,
          checkArticle: editorChanged ? Boolean(currentArticle && !editorVisibilityKnown) : pendingRefresh.checkArticle,
          backTarget: safeEditorBackLink()};
      }
      activeID = 0;
      activeSession = '';
      editorReady = false;
      legacyEditor = false;
      editorChanged = false;
      if (pendingRefresh) refreshSavedPage(pendingRefresh, editorGeneration);
    };
    const closeEditorDialog = () => {
      dialog.close();
      finishEditorClose();
    };
    const requestClose = () => {
      if (!isOpen()) return;
      detectLegacyEditor();
      if (!editorReady) { closeEditorDialog(); return; }
      const message = {type: 'ganwen-editor-close-request', id: activeID};
      if (!legacyEditor) message.session = activeSession;
      frame.contentWindow?.postMessage(message, location.origin);
    };
    if (editable.length) {
      for (const button of editable) button.addEventListener('click', () => {
        if (authentication !== true || isOpen()) return;
        activeID = postID(button);
        activeButton = button;
        editorGeneration++;
        refreshController?.abort();
        activeSession = randomIdentifier() || String(editorGeneration);
        editorReady = false;
        legacyEditor = false;
        editorChanged = false;
        editorPublic = true;
        editorVisibilityKnown = false;
        editorPublicTarget = null;
        frame.src = '/admin/?embed=1&edit=' + activeID + '&editor_session=' + encodeURIComponent(activeSession) + '&editor_language=' + publicLanguage;
        dialog.showModal();
        document.body.classList.add('has-article-editor');
      });
      closeButton?.addEventListener('click', requestClose);
      frame.addEventListener('load', detectLegacyEditor);
      dialog.addEventListener('cancel', event => { event.preventDefault(); requestClose(); });
      dialog.addEventListener('close', () => {
        if (!isOpen()) finishEditorClose();
      });
      addEventListener('message', event => {
        if (!isOpen() || event.origin !== location.origin || event.source !== frame.contentWindow) return;
        const message = event.data;
        if (!message || typeof message !== 'object' || Number(message.id) !== activeID) return;
        detectLegacyEditor();
        if (message.session !== activeSession && !(legacyEditor && message.session === undefined
            && ['ganwen-editor-saved', 'ganwen-editor-close'].includes(message.type))) return;
        if (message.type === 'ganwen-editor-ready') editorReady = true;
        else if (message.type === 'ganwen-editor-saved') {
          editorChanged = true;
          editorPublic = (typeof message.status !== 'string' || message.status === 'publish')
            && (!Array.isArray(message.publicLanguages) || message.publicLanguages.includes(publicLanguage));
          editorVisibilityKnown = Array.isArray(message.publicLanguages) || typeof message.status === 'string'
            && (message.status !== 'publish' || publicLanguage === 'zh');
          const currentArticle = document.querySelector('.article-card') && Number(main?.dataset.postId) === activeID;
          const path = message.publicPath;
          if (currentArticle && typeof path === 'string') {
            const prefix = publicLanguage === 'zh' ? '' : '/' + publicLanguage;
            if (/^\/[A-Za-z0-9][A-Za-z0-9/_-]*(?:\.html)?\/?$/.test(path) && !path.includes('//')) {
              editorPublicTarget = prefix + path + location.hash;
            } else if (path === '' && main?.dataset.independentPage === 'true') {
              editorPublicTarget = prefix + '/?page_id=' + activeID + location.hash;
            }
          }
        }
        else if (message.type === 'ganwen-editor-close') closeEditorDialog();
      });
    }
    const recordPageView = () => {
      if (authentication !== false || sent || navigator.globalPrivacyControl === true) return;
      if ([navigator.doNotTrack, window.doNotTrack, navigator.msDoNotTrack].some(value => value === '1' || value === 'yes')) return;
      const path = analyticsPath(location.href);
      const eventID = randomIdentifier();
      const identifiers = anonymousIdentifiers();
      if (!path || !eventID || !identifiers) return;
      const language = supportedLanguages().has(main?.dataset.publicLanguage) ? main.dataset.publicLanguage : 'zh';
      let referrer = '';
      try {
        const source = new URL(document.referrer);
        if (['http:', 'https:'].includes(source.protocol)) referrer = source.origin;
      } catch {}
      const body = JSON.stringify({event_id: eventID, ...identifiers, path, language, referrer});
      sent = true;
      if (typeof navigator.sendBeacon === 'function' && navigator.sendBeacon('/analytics/event', new Blob([body], {type: 'application/json'}))) return;
      fetch('/analytics/event', {method: 'POST', credentials: 'same-origin', cache: 'no-store', keepalive: true,
        headers: {'Content-Type': 'application/json'}, body}).catch(() => {});
    };
    let probing = false;
    let probePending = false;
    const showSessionControls = authenticated => {
      for (const button of buttons) button.hidden = authenticated !== true || !editableButtons.has(button);
      for (const link of document.querySelectorAll('[data-admin-link]')) link.hidden = authenticated !== true;
    };
    const probeSession = (invalidate = false) => {
      if (invalidate) { authentication = null; showSessionControls(false); }
      if (probing) { probePending = true; return; }
      probing = true;
      showSessionControls(authentication);
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), 5000);
      fetch('/admin/api/editor-session', {credentials: 'same-origin', cache: 'no-store',
        headers: {Accept: 'application/json'}, signal: controller.signal})
        .then(response => response.ok ? response.json() : null)
        .then(session => {
          if (probePending || typeof session?.authenticated !== 'boolean') return;
          authentication = session.authenticated;
          showSessionControls(authentication);
          if (authentication) {
            restoreEditorFocus();
          } else recordPageView();
        }).catch(() => {}).finally(() => {
          clearTimeout(timeout); probing = false;
          if (probePending) { probePending = false; probeSession(); }
        });
    };
    addEventListener('pageshow', event => {
      if (event.persisted) probeSession(true);
      else {
        recordPageView();
        try {
          const position = JSON.parse(sessionStorage.getItem(positionKey));
          sessionStorage.removeItem(positionKey);
          if (position?.url === location.pathname + location.search + location.hash && Number.isFinite(position.scroll)
              && position.scroll >= 0 && Number.isFinite(position.savedAt) && Date.now() - position.savedAt <= 60000) {
            restoreFocusID = Number(position.postId);
            requestAnimationFrame(() => scrollTo({top: position.scroll, behavior: 'instant'}));
            restoreEditorFocus();
          }
        } catch {}
      }
    });
    addEventListener('focus', () => probeSession());
    addEventListener('online', () => probeSession());
    addEventListener('storage', event => {
      if (event.key === 'ganwen.admin-session.changed' || event.key === null) probeSession(true);
    });
    document.addEventListener('visibilitychange', () => {
      if (document.visibilityState === 'visible') probeSession();
    });
    probeSession();
    return {isOpen, requestClose};
  }
  function initializeLanguagePicker() {
    const picker = document.querySelector('[data-language-picker]');
    if (!picker) return;
    const summary = picker.querySelector('summary');
    const links = Array.from(picker.querySelectorAll('.language-links a[lang]'));
    if (!summary || !links.length) return;
    picker.addEventListener('keydown', event => {
      if (event.defaultPrevented || event.ctrlKey || event.altKey || event.metaKey || event.isComposing) return;
      const active = document.activeElement;
      const current = links.indexOf(active);
      if (event.key === 'Escape' && picker.open) {
        event.preventDefault();
        picker.open = false;
        summary.focus({preventScroll: true});
        return;
      }
      if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
      if (active !== summary && current < 0) return;
      event.preventDefault();
      picker.open = true;
      let index;
      if (event.key === 'Home') index = 0;
      else if (event.key === 'End') index = links.length - 1;
      else if (current >= 0) index = (current + (event.key === 'ArrowDown' ? 1 : -1) + links.length) % links.length;
      else index = event.key === 'ArrowDown' ? Math.max(0, links.findIndex(link => link.getAttribute('aria-current') === 'true')) : links.length - 1;
      links[index].focus({preventScroll: true});
    });
    document.addEventListener('click', event => {
      if (picker.open && !picker.contains(event.target)) picker.open = false;
    });
    document.addEventListener('focusin', event => {
      if (picker.open && !picker.contains(event.target)) picker.open = false;
    });
  }
  function initializeArticleTOC() {
    const toc = document.querySelector('[data-article-toc]');
    const links = document.querySelector('[data-toc-links]');
    if (!toc || !links) return;
    const headings = Array.from(document.querySelectorAll('.article-body h2, .article-body h3'))
      .filter(heading => heading.textContent.trim());
    if (headings.length < 2) return;
    const usedIDs = new Set(Array.from(document.querySelectorAll('[id]'), element => element.id));
    headings.forEach((heading, index) => {
      if (!heading.id) {
        const base = 'article-section-' + (index + 1);
        let id = base;
        let suffix = 2;
        while (usedIDs.has(id)) id = base + '-' + suffix++;
        heading.id = id;
        usedIDs.add(id);
      }
      const link = document.createElement('a');
      link.href = '#' + encodeURIComponent(heading.id);
      link.textContent = heading.textContent.trim();
      if (heading.tagName === 'H3') link.className = 'toc-subheading';
      links.append(link);
    });
    toc.hidden = false;
  }
  function initializePage() {
  initializeLanguagePicker();
  initializeArticleTOC();
  for (const button of document.querySelectorAll('[data-theme-choice]')) {
    button.addEventListener('click', () => {
      themeMode = button.dataset.themeChoice;
      try { localStorage.setItem(themeKey, themeMode); } catch {}
      applyTheme();
    });
  }
  applyTheme();
  const overviewGrid = document.querySelector('.overview-page .post-grid');
  if (overviewGrid) {
    const cards = Array.from(overviewGrid.querySelectorAll(':scope > .category-card'));
    let gridWidth = 0;
    let pendingFrame = 0;
    const arrangeCards = () => {
      pendingFrame = 0;
      if (!cards.length) return;
      const width = overviewGrid.getBoundingClientRect().width;
      if (width === gridWidth) return;
      gridWidth = width;
      const desired = parseInt(getComputedStyle(overviewGrid).getPropertyValue('--desired-columns'), 10) || 1;
      const count = Math.max(1, Math.min(desired, cards.length));
      overviewGrid.dataset.columns = String(count);
      const columns = Array.from({length: count}, () => {
        const column = document.createElement('div');
        column.className = 'post-column';
        return column;
      });
      overviewGrid.replaceChildren(...columns);
      const heights = new Array(count).fill(0);
      for (const card of cards) {
        const index = heights.indexOf(Math.min(...heights));
        columns[index].append(card);
        heights[index] = columns[index].getBoundingClientRect().height;
      }
    };
    const scheduleCards = () => {
      if (!pendingFrame) pendingFrame = requestAnimationFrame(arrangeCards);
    };
    arrangeCards();
    if (typeof ResizeObserver === 'function') new ResizeObserver(scheduleCards).observe(overviewGrid);
    else addEventListener('resize', scheduleCards);
  }
  for (const image of document.querySelectorAll('[data-avatar-image], [data-contact-image]')) {
    const setReady = value => {
      image.classList.toggle('is-ready', value);
      const fallback = image.parentElement.querySelector('[data-avatar-fallback], [data-contact-fallback]');
      if (fallback) fallback.hidden = value;
    };
    const ready = () => setReady(image.complete && image.naturalWidth > 0);
    image.addEventListener('load', ready);
    image.addEventListener('error', () => setReady(false));
    if (image.complete) ready();
  }
  for (const link of document.querySelectorAll('.language-links a[lang]')) {
    link.addEventListener('click', () => {
      const language = link.getAttribute('lang');
      if (!supportedLanguages().has(language)) return;
      try {
        if (new URL(link.href, location.origin).origin !== location.origin) return;
        document.cookie = 'ganwen_language=' + language + '; Path=/; Max-Age=31536000; SameSite=Lax' + (location.protocol === 'https:' ? '; Secure' : '');
      } catch {}
    });
  }
  const menu = document.querySelector('.menu-toggle');
  const searchToggle = document.querySelector('.search-toggle');
  const search = document.getElementById('site-search');
  const input = document.getElementById('search-text');
  function showSearch(expanded) {
    if (!search) return;
    search.hidden = !expanded;
    searchToggle?.setAttribute('aria-expanded', String(expanded));
  }
  if (search) showSearch(search.dataset.searchOpen === 'true');
  menu?.addEventListener('click', () => {
    const expanded = menu.getAttribute('aria-expanded') === 'true';
    menu.setAttribute('aria-expanded', String(!expanded));
    document.getElementById('main-nav').classList.toggle('open', !expanded);
  });
  searchToggle?.addEventListener('click', () => {
    if (!search) return;
    showSearch(search.hidden);
    if (!search.hidden) input?.focus();
  });

  const stateKey = 'ganwen.list-position.v1';
  const layoutPositionKey = 'ganwen.layout-position.v1';
  const isArticle = Boolean(document.querySelector('.article-card'));
  const language = document.querySelector('[data-public-language]')?.dataset.publicLanguage || 'zh';
  function archiveURL(value) {
    try {
      const url = new URL(value, location.origin);
      if (url.origin !== location.origin || !isPublicPath(url.pathname)) return null;
      for (const key of ['p', 'page_id', 'attachment_id', 'feed', 'sitemap', 'robots']) {
        if (url.searchParams.has(key)) return null;
      }
      return url.pathname + url.search + url.hash;
    } catch { return null; }
  }
  function storedList() {
    try {
      const state = JSON.parse(sessionStorage.getItem(stateKey));
      if (!state || !archiveURL(state.url) || !Number.isFinite(state.scroll) || state.scroll < 0 || !Number.isFinite(state.savedAt) || Date.now() - state.savedAt > 86400000) return null;
      const storedLanguage = /^\/([a-z]{2})(?:\/|$)/.exec(new URL(state.url, location.origin).pathname)?.[1] || 'zh';
      if (storedLanguage !== language) return null;
      return state;
    } catch { return null; }
  }
  function rememberList() {
    if (isArticle) return;
    const url = archiveURL(location.href);
    if (!url) return;
    try { sessionStorage.setItem(stateKey, JSON.stringify({url, scroll: scrollY, savedAt: Date.now()})); } catch {}
  }
  document.querySelector('[data-layout-picker]')?.addEventListener('change', event => {
    const layout = event.target.value;
    if (!['journal', 'classic'].includes(layout)) return;
    try {
      document.cookie = 'ganwen.layout.v1=' + layout + '; Path=/; Max-Age=31536000; SameSite=Lax' + (location.protocol === 'https:' ? '; Secure' : '');
    } catch { return; }
    rememberList();
    try {
      sessionStorage.setItem(layoutPositionKey, JSON.stringify({url: location.pathname + location.search + location.hash, scroll: scrollY, savedAt: Date.now()}));
    } catch {}
    location.reload();
  });
  addEventListener('pageshow', event => {
    if (event.persisted) return;
    try {
      const state = JSON.parse(sessionStorage.getItem(layoutPositionKey));
      sessionStorage.removeItem(layoutPositionKey);
      if (state?.url === location.pathname + location.search + location.hash && Number.isFinite(state.scroll) && state.scroll >= 0 && Number.isFinite(state.savedAt) && Date.now() - state.savedAt <= 60000) {
        requestAnimationFrame(() => scrollTo({top: state.scroll, behavior: 'instant'}));
      }
    } catch {}
  });
  if (!isArticle) {
    addEventListener('pagehide', rememberList);
    document.addEventListener('click', event => {
      if (event.target.closest('a[href]')) rememberList();
    });
    addEventListener('pageshow', event => {
      const state = storedList();
      if (!event.persisted && !location.hash && state?.url === location.pathname + location.search) {
        requestAnimationFrame(() => scrollTo({top: state.scroll, behavior: 'instant'}));
      }
    });
  } else {
    const state = document.querySelector('[data-independent-page]') ? null : storedList();
    for (const back of document.querySelectorAll('[data-back-link]')) {
      if (state) back.setAttribute('href', state.url);
      back.addEventListener('click', event => {
        if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) return;
        const from = archiveURL(document.referrer);
        if (from && state && from === state.url && history.length > 1) {
          event.preventDefault();
          history.back();
        }
      });
    }
  }
  document.querySelector('[data-scroll-top]')?.addEventListener('click', () => scrollTo({top: 0, behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth'}));
  const editorUI = initializePublicSession(archiveURL);
  document.addEventListener('keydown', event => {
    if (event.defaultPrevented || event.ctrlKey || event.altKey || event.metaKey || event.isComposing) return;
    if (editorUI.isOpen()) {
      if (event.key === 'Escape') { event.preventDefault(); editorUI.requestClose(); }
      return;
    }
    const active = document.activeElement;
    if (active?.closest('.intro-contacts') || event.target?.closest?.('.intro-contacts')) return;
    if (document.querySelector('[data-language-picker]')?.open && active?.closest('[data-language-picker]')) return;
    if (event.key === 'Escape') {
      const help = document.querySelector('[data-shortcuts]');
      if (help?.open) {
        event.preventDefault();
        help.open = false;
        help.querySelector('summary')?.focus({preventScroll: true});
        return;
      }
      if (search && !search.hidden) {
        event.preventDefault();
        showSearch(false);
        searchToggle?.focus({preventScroll: true});
        return;
      }
    }
    const interactive = 'input, textarea, select, button, summary, audio, video, [contenteditable]:not([contenteditable="false"]), [role="textbox"], [role="slider"], [role="spinbutton"], [role="tab"], [role="combobox"], [role="listbox"], [role="menu"], [role="tree"], [role="grid"], [role="radio"], [role="switch"]';
    if (active?.isContentEditable || active?.closest(interactive) || event.target?.closest?.(interactive)) return;
    if (getSelection()?.toString()) return;
    if (event.key === '/' && search) {
      event.preventDefault();
      showSearch(true);
      input?.focus();
    } else if (event.key === '?') {
      const help = document.querySelector('[data-shortcuts]');
      if (help) { event.preventDefault(); help.open = !help.open; }
    } else if (event.key === 'Escape' && isArticle) {
      event.preventDefault();
      document.querySelector('[data-back-link]')?.click();
    } else if (event.key === 'ArrowLeft' || event.key === 'ArrowRight') {
      const link = document.querySelector(event.key === 'ArrowLeft' ? '[data-previous]' : '[data-next]');
      if (link) { event.preventDefault(); link.click(); }
    }
  });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', initializePage, {once: true});
  else initializePage();
})();
