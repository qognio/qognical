/* qognical embed loader — vanilla, no deps, ~5KB.
 * Served by the qognical instance at /embed.js. Embedding sites include it
 * with <script src="https://book.example.com/embed.js" async></script> and
 * place [data-qognical-link="<host>/<event-type>"] elements on the page.
 *
 * Modes (data-qognical-mode):
 *   inline   render as inline iframe (default)
 *   popup    open the booking flow in a modal on element click
 *   floating render a fixed-position pill button
 *
 * Event bus (window.Qognical.on(name, handler)):
 *   embed.ready, slot.selected, intake.submitted, payment.redirecting,
 *   booking.successful, booking.failed, embed.resize
 *
 * All postMessage exchange is origin-checked against the script's source.
 */
(function (window, document) {
  if (window.Qognical) return; // double-include guard

  // Where this script came from = trusted origin.
  var script = document.currentScript;
  if (!script) {
    var scripts = document.getElementsByTagName('script');
    for (var i = scripts.length - 1; i >= 0; i--) {
      if (/\/embed\.js(\?|$)/.test(scripts[i].src)) { script = scripts[i]; break; }
    }
  }
  var origin = (function () {
    if (!script || !script.src) return window.location.origin;
    var a = document.createElement('a'); a.href = script.src;
    return a.origin || (a.protocol + '//' + a.host);
  })();

  var listeners = {};
  function emit(event, payload) {
    (listeners[event] || []).forEach(function (fn) {
      try { fn(payload); } catch (e) { console.error('qognical handler', e); }
    });
  }
  function on(event, fn) {
    (listeners[event] = listeners[event] || []).push(fn);
  }
  function off(event, fn) {
    var arr = listeners[event] || [];
    listeners[event] = arr.filter(function (f) { return f !== fn; });
  }

  // global postMessage router — every embedded iframe sends to its parent
  window.addEventListener('message', function (ev) {
    if (ev.origin !== origin) return;
    var msg = ev.data || {};
    if (!msg || typeof msg !== 'object' || msg.source !== 'qognical') return;
    if (msg.event === 'embed.resize' && msg.iframeId) {
      var iframe = document.getElementById(msg.iframeId);
      if (iframe && typeof msg.height === 'number') {
        iframe.style.height = msg.height + 'px';
      }
    }
    emit(msg.event, msg.payload);
  });

  var counter = 0;
  function newId() {
    counter += 1;
    return 'qognical-iframe-' + counter + '-' + Math.random().toString(36).slice(2, 8);
  }

  function buildIframeURL(link, opts) {
    var params = new URLSearchParams();
    if (opts.theme)      params.set('theme', opts.theme);
    if (opts.brandColor) params.set('brand', opts.brandColor);
    params.set('embed', '1');
    var qs = params.toString();
    return origin + '/book/' + link + (qs ? '?' + qs : '');
  }

  function makeIframe(link, opts) {
    var id = newId();
    var iframe = document.createElement('iframe');
    iframe.id = id;
    iframe.src = buildIframeURL(link, opts);
    iframe.style.border = '0';
    iframe.style.width = '100%';
    iframe.style.minHeight = '480px';
    iframe.setAttribute('allow', 'payment');
    iframe.setAttribute('title', 'qognical booking');
    // Pass iframe id to the SPA so its resize events can target this element.
    iframe.addEventListener('load', function () {
      try {
        iframe.contentWindow.postMessage({
          source: 'qognical-parent',
          event: 'init',
          iframeId: id,
        }, origin);
      } catch (e) { /* cross-origin OK */ }
    });
    return iframe;
  }

  // --- inline mode ---
  function mountInline(el) {
    var link = el.getAttribute('data-qognical-link');
    if (!link) return;
    var iframe = makeIframe(link, {
      theme: el.getAttribute('data-qognical-theme'),
      brandColor: el.getAttribute('data-qognical-brand-color'),
    });
    el.innerHTML = '';
    el.appendChild(iframe);
  }

  // --- popup mode ---
  function openPopup(link, opts) {
    var overlay = document.createElement('div');
    overlay.setAttribute('data-qognical-overlay', '');
    Object.assign(overlay.style, {
      position: 'fixed', inset: '0',
      background: 'rgba(20, 20, 26, 0.7)',
      display: 'flex', alignItems: 'center', justifyContent: 'center',
      zIndex: '2147483647',
    });
    var card = document.createElement('div');
    Object.assign(card.style, {
      background: '#fff', borderRadius: '12px', maxWidth: '760px',
      width: 'calc(100% - 32px)', height: '90vh',
      overflow: 'hidden', position: 'relative',
      boxShadow: '0 24px 64px rgba(0,0,0,0.4)',
    });
    var close = document.createElement('button');
    close.textContent = '×';
    Object.assign(close.style, {
      position: 'absolute', top: '8px', right: '12px',
      background: 'transparent', border: '0',
      fontSize: '28px', cursor: 'pointer', color: '#444',
    });
    close.addEventListener('click', function () { document.body.removeChild(overlay); });
    overlay.addEventListener('click', function (e) {
      if (e.target === overlay) document.body.removeChild(overlay);
    });
    var iframe = makeIframe(link, opts);
    iframe.style.height = '100%';
    iframe.style.minHeight = '0';
    card.appendChild(close);
    card.appendChild(iframe);
    overlay.appendChild(card);
    document.body.appendChild(overlay);
    on('booking.successful', function () {
      // Auto-close 3s after a successful booking.
      setTimeout(function () {
        if (overlay.parentNode) overlay.parentNode.removeChild(overlay);
      }, 3000);
    });
  }
  function mountPopupTrigger(el) {
    var link = el.getAttribute('data-qognical-trigger');
    if (!link) return;
    el.addEventListener('click', function (e) {
      e.preventDefault();
      openPopup(link, {
        theme: el.getAttribute('data-qognical-theme'),
        brandColor: el.getAttribute('data-qognical-brand-color'),
      });
    });
  }

  // --- floating button (imperative API) ---
  function floatingButton(cfg) {
    if (!cfg || !cfg.link) throw new Error('Qognical.floatingButton: link required');
    var btn = document.createElement('button');
    btn.textContent = cfg.label || 'Termin buchen';
    var pos = cfg.position || 'bottom-right';
    var positions = {
      'bottom-right': { right: '20px', bottom: '20px' },
      'bottom-left':  { left:  '20px', bottom: '20px' },
      'top-right':    { right: '20px', top: '20px' },
      'top-left':     { left:  '20px', top: '20px' },
    };
    Object.assign(btn.style, positions[pos] || positions['bottom-right'], {
      position: 'fixed', zIndex: '2147483646',
      background: cfg.brandColor || '#2B5ADC',
      color: '#fff', border: '0', borderRadius: '999px',
      padding: '12px 22px', fontSize: '15px', fontWeight: '600',
      cursor: 'pointer',
      boxShadow: '0 6px 20px rgba(0,0,0,0.18)',
    });
    btn.addEventListener('click', function () {
      openPopup(cfg.link, { theme: cfg.theme, brandColor: cfg.brandColor });
    });
    document.body.appendChild(btn);
    return btn;
  }

  function init() {
    document.querySelectorAll('[data-qognical-link]').forEach(function (el) {
      var mode = el.getAttribute('data-qognical-mode') || 'inline';
      if (mode === 'inline')       mountInline(el);
      else if (mode === 'popup-trigger') mountPopupTrigger(el);
    });
    document.querySelectorAll('[data-qognical-trigger]').forEach(mountPopupTrigger);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }

  window.Qognical = {
    on: on,
    off: off,
    open: openPopup,
    floatingButton: floatingButton,
    init: init,
    origin: origin,
  };
})(window, document);
