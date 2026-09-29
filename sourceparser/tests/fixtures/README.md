# Manually checked JS/TS provenance corpus

The representative repository has no TypeScript. `reservations.ts` is independent synthetic source, never executed or installed. Both fixture files preserve UTF-8 and CRLF. Local `.gitattributes` disables newline conversion.

`booking.js` has six physical lines. `createBooking` occupies original UTF-8 bytes [62,137), display lines 3–5; its signature is `async function createBooking(名称)`. The leading import is line 1, and the arrow export `cancelBooking` is line 6. Chinese text and an emoji before the declaration distinguish UTF-8 bytes from character/UTF-16 coordinates.

`reservations.ts` has fourteen physical lines. Line 1 imports a type; lines 2–3 export a type alias and interface. The namespace `Reservations` contains generic class `Scheduler<T extends Booking>` and async method `reserve`. Class annotation `@sealed` begins at byte 183, line 5. Method annotation `@trace` occupies bytes [243,249), line 7; the method evidence starts at byte 243, includes that annotation, and its signature is `@trace\r\n    async reserve(request: Request<T>): Promise<T>`. The qualified parent structure is `<logical path>.Reservations.Scheduler.reserve`. The namespace also contains the `cancel` arrow function; line 14 re-exports a named binding with an alias.

HTTP expectations are literal, and the Go integration suite consumes these files through real Git, parser HTTP, keyword/vector indexes and public readers/tools. Additional HTTP examples cover object methods typical of Vue 2 modules (without parsing SFC), anonymous default exports, class callable fields, JSX/TSX, invalid syntax/BOM and oversized Unicode.
