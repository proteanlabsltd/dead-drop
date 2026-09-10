# Dead Drop protocol v1

## Wire protocol (v1)

All responses are JSON unless streaming file bytes. Paths are absolute within the daemon's view (e.g. `/home/axl/srv`); the daemon maps them to real paths only after confinement checks. Errors: `{"error":{"code":"not_found|forbidden|exists|invalid_path|io|bad_request","message":"…"}}` with matching HTTP status (404/403/409/400/500).

Method · Path | Request | Response
--- | --- | ---
`GET /v1/info` | — | `{"name":"vps-fra1","version":"0.1.0","protocol":1,"roots":["/home/axl"],"user":"axl@github"}` — `user` is the caller's identity as seen by whois (useful for debugging 403s).
`GET /v1/ls?path=` | Directory path | `{"path":"/home/axl/srv","entries":[{"name":"backups","type":"dir","size":4096,"mtime":"2026-09-10T18:02:11Z","mode":"drwxr-xr-x"},{"name":"nginx.conf","type":"file","size":2147,…}]}`. `type` ∈ `file|dir|symlink|other`; symlinks report their target type in `link_type`. Sorted dirs-first, case-insensitive.
`GET /v1/stat?path=` | Any path | Single entry object as above.
`GET /v1/read?path=` | File path. Honours `Range: bytes=N-`. | Raw bytes, `Content-Length`, `Last-Modified`, `ETag` (size-mtime), `Accept-Ranges: bytes`. 206 on range.
`PUT /v1/write?path=&offset=0&overwrite=0` | Raw body, `Content-Length` required. Optional `X-Expected-Size` (total) and `X-Content-SHA256` (verified on final chunk). | Writes to `<path>.deaddrop-part`; when written bytes == expected size, fsync + rename. Returns the final `stat` entry, or `{"partial":true,"size":N}`. 409 `exists` if the target exists and `overwrite=0`. `HEAD /v1/write?path=` returns `X-Partial-Size` so clients can resume.
`POST /v1/mkdir` | `{"path":"…"}` | Entry of new directory; 409 if it exists.

Every request is authenticated before routing: the daemon extracts the peer IP, calls `GET /localapi/v0/whois?addr=IP:port` on the tailscaled unix socket, and requires `UserProfile.LoginName ∈ allow_users` (or a node tag in `allow_tags`). Non-tailnet addresses get no whois answer and are rejected with 403 before any filesystem access. Results are cached per peer IP for 60 s.


## Clarifications

`DELETE /v1/write?path=` cancels an upload by removing only the associated `.deaddrop-part` file, never the final target. Listings cap at 5,000 entries and return `truncated: true` when capped. `HEAD /v1/write` returns `X-Partial-Size: 0` if no partial exists. Missing upload Content-Length returns 411. Authentication applies to all methods including unknown routes. Clients must connect directly without an HTTP proxy and must not follow redirects.
