// @qognical/embed-react — React wrapper around the embed.js loader served
// by a qognical instance. SSR-safe (no window access at module scope, all
// browser work happens inside useEffect).
//
// Two surfaces:
//   <QognicalEmbed instance="https://book.example.com" link="finn/erst" />
//   const api = await getQognicalApi('https://book.example.com');
//   api.on('booking.successful', e => ...);

import { useEffect, useRef } from 'react';

export type EmbedEvent =
  | 'embed.ready'
  | 'slot.selected'
  | 'intake.submitted'
  | 'payment.redirecting'
  | 'booking.successful'
  | 'booking.failed'
  | 'embed.resize';

export interface QognicalApi {
  on(event: EmbedEvent, handler: (payload: any) => void): void;
  off(event: EmbedEvent, handler: (payload: any) => void): void;
  open(link: string, opts?: { theme?: string; brandColor?: string }): void;
  floatingButton(cfg: {
    link: string;
    label?: string;
    position?: 'bottom-right' | 'bottom-left' | 'top-right' | 'top-left';
    theme?: string;
    brandColor?: string;
  }): HTMLButtonElement;
  init(): void;
  origin: string;
}

declare global {
  interface Window {
    Qognical?: QognicalApi;
  }
}

const loadingCache = new Map<string, Promise<QognicalApi>>();

/** Lazily injects <script src="{instance}/embed.js"> and resolves with the
 *  global Qognical API. Idempotent per-instance. SSR-safe (returns a
 *  pending promise on the server; only the browser actually loads). */
export function getQognicalApi(instance: string): Promise<QognicalApi> {
  if (typeof window === 'undefined') {
    // On the server, never resolve — the consuming component runs the
    // effect on hydration which will start the real load.
    return new Promise<QognicalApi>(() => {});
  }
  const cached = loadingCache.get(instance);
  if (cached) return cached;
  const promise = new Promise<QognicalApi>((resolve, reject) => {
    if (window.Qognical) {
      resolve(window.Qognical);
      return;
    }
    const script = document.createElement('script');
    script.src = instance.replace(/\/$/, '') + '/embed.js';
    script.async = true;
    script.onload = () => {
      if (window.Qognical) resolve(window.Qognical);
      else reject(new Error('Qognical did not appear on window after load'));
    };
    script.onerror = () => reject(new Error('Failed to load qognical/embed.js'));
    document.head.appendChild(script);
  });
  loadingCache.set(instance, promise);
  return promise;
}

export interface QognicalEmbedProps {
  /** qognical instance origin, e.g. https://book.example.com */
  instance: string;
  /** "<host>/<event-type-slug>" */
  link: string;
  /** Inline (default) → renders an iframe in place.
   *  Popup → renders a button that opens a modal on click. */
  mode?: 'inline' | 'popup';
  buttonLabel?: string;
  theme?: 'light' | 'dark';
  brandColor?: string;
  /** Event handlers; cleaned up on unmount. */
  onReady?: (e: { version: string }) => void;
  onSlotSelected?: (e: { start_utc: string }) => void;
  onBookingSuccess?: (e: { booking_id: string }) => void;
  onBookingFailed?: (e: { error_code: string }) => void;
  className?: string;
  style?: React.CSSProperties;
}

export function QognicalEmbed({
  instance,
  link,
  mode = 'inline',
  buttonLabel = 'Termin buchen',
  theme,
  brandColor,
  onReady,
  onSlotSelected,
  onBookingSuccess,
  onBookingFailed,
  className,
  style,
}: QognicalEmbedProps) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let cancelled = false;
    let api: QognicalApi | undefined;
    const handlers: Array<[EmbedEvent, (e: any) => void]> = [];

    getQognicalApi(instance).then((a) => {
      if (cancelled) return;
      api = a;
      // Hook up declarative props as event listeners.
      const pairs: Array<[EmbedEvent, ((e: any) => void) | undefined]> = [
        ['embed.ready', onReady],
        ['slot.selected', onSlotSelected],
        ['booking.successful', onBookingSuccess],
        ['booking.failed', onBookingFailed],
      ];
      for (const [evt, fn] of pairs) {
        if (!fn) continue;
        api.on(evt, fn);
        handlers.push([evt, fn]);
      }
      // Trigger init for whatever container ref points at.
      if (mode === 'inline' && ref.current) {
        ref.current.setAttribute('data-qognical-link', link);
        ref.current.setAttribute('data-qognical-mode', 'inline');
        if (theme) ref.current.setAttribute('data-qognical-theme', theme);
        if (brandColor) ref.current.setAttribute('data-qognical-brand-color', brandColor);
        api.init();
      }
    });

    return () => {
      cancelled = true;
      if (api) {
        for (const [evt, fn] of handlers) api.off(evt, fn);
      }
    };
  }, [instance, link, mode, theme, brandColor]);

  if (mode === 'popup') {
    return (
      <button
        className={className}
        style={{
          background: brandColor || '#2B5ADC',
          color: '#fff',
          border: 0,
          borderRadius: 8,
          padding: '10px 18px',
          fontSize: 15,
          fontWeight: 600,
          cursor: 'pointer',
          ...style,
        }}
        onClick={() => {
          getQognicalApi(instance).then((api) =>
            api.open(link, { theme, brandColor }),
          );
        }}
      >
        {buttonLabel}
      </button>
    );
  }

  return <div ref={ref} className={className} style={style} />;
}

export default QognicalEmbed;
