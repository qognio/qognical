# @qognical/embed-react

React wrapper for the qognical booking embed. SSR-safe, ~3 KB after gzip.

## Install

```bash
npm install @qognical/embed-react
```

## Usage — Inline

```jsx
import { QognicalEmbed } from '@qognical/embed-react';

export default function ContactPage() {
  return (
    <QognicalEmbed
      instance="https://book.example.com"
      link="finn/erstgespraech"
      onBookingSuccess={(e) => analytics.track('booking', e)}
    />
  );
}
```

## Usage — Popup button

```jsx
<QognicalEmbed
  instance="https://book.example.com"
  link="finn/erstgespraech"
  mode="popup"
  buttonLabel="Termin vereinbaren"
  brandColor="#D7263D"
/>
```

## Imperative API

```ts
import { getQognicalApi } from '@qognical/embed-react';

const api = await getQognicalApi('https://book.example.com');
api.on('booking.successful', (e) => { /* … */ });
api.floatingButton({ link: 'finn/erstgespraech', position: 'bottom-right' });
```

## Events

| Event | Payload |
|---|---|
| `embed.ready` | `{ version }` |
| `slot.selected` | `{ start_utc }` |
| `intake.submitted` | `{ event_type_id }` |
| `payment.redirecting` | `{ provider }` |
| `booking.successful` | `{ booking_id }` |
| `booking.failed` | `{ error_code }` |
| `embed.resize` | `{ height }` (parent loader handles internally) |

## SSR

`getQognicalApi` returns a promise that never resolves on the server; the
real load happens on first client effect. Wrap usage in `useEffect` (which
`<QognicalEmbed>` already does internally).

## License

Apache-2.0
