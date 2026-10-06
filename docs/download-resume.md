# Resumable stored downloads

Local download-center files and customer files support `HEAD`, single and multipart `Range: bytes=...`, and `If-Range` on their existing file URLs. Service-scoped download URLs use the same download-center handler. A successful partial response is `206` with `Content-Range`; an unsatisfiable range is `416`. More than 16 ranges or an 8 KiB range header is rejected with `416` to bound multipart work. A stale `If-Range` returns the full current file.

The quoted SHA-256 content hash is the strong `ETag`. File replacement changes the tag. Every request passes the existing session or API-key, visibility, ownership, and product or sales-scope checks before file headers or bytes are sent. File responses retain `Cache-Control: private, no-store`, attachment disposition, and `nosniff`.

As before, every authorized `GET` attempt advances download counters, including range errors and conditional requests; `HEAD` does not. Customer file `GET` attempts also retain their audit event. Remote URL downloads still return their existing JSON response and do not support byte ranges.
