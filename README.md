# Lazyraster
Lazyraster is a HTTP service to convert PDF pages into PNG built on top of <a href="https://github.com/nitro/lazypdf">lazypdf</a>.

## Run
Environment variables:
| Options | Description |
| ----------------------- | ------------------------------------------------------------------------------------- |
| `URL_SIGNING_SECRET` | Secret used to check if the request is valid. |
| `ENABLE_DATADOG` | Enable Datadog. |
| `STORAGE_BUCKET_REGION` | Map of the region a bucket belongs to: `eu-west-1:bucket1,bucket2;us-west-1:bucket3`. |
| `PAGE_CACHE_BUCKET` | Bucket holding rendered pages. Empty disables the page cache. |
| `PAGE_CACHE_REGION` | Region of `PAGE_CACHE_BUCKET`. Required when the bucket is set. |
| `PAGE_CACHE_PREFIX` | Optional key prefix inside `PAGE_CACHE_BUCKET`. |

```go
go run cmd/main.go
```

## Testing
```go
go test -v -race -cover ./...
```

## Rendered page cache

`POST /render` (the SWS-direct envelopes path) caches its output in S3. Every input that affects the
rendered bytes — source object, `version`, page, width, dpi, scale, format and the annotations — is
hashed into the object key, so an entry is content-addressed: the same request can only ever describe
one image. A page rendered for one signer is served to the next without fetching the document or
entering lazypdf.

`version` is the caller's content version, opaque here (SWS sends its `v`: the document's
last-modified stamp plus a digest of its field values). It is also the key's first path component in
the clear — `v1/<version>/<hash>.png` — so everything cached for one document version can be listed
or dropped as a unit, and the same token appears in the page URL, SWS's render-context cache and here.
Because it covers the whole document, any field change re-renders every page of it rather than just
the page that changed; that is a deliberate trade for one debuggable version across all three caches.

Two properties are load-bearing:

- **The key carries no caller identity.** No package, actor or user takes part in it, so a key cannot
  grant access to a document the caller could not already render. Authorization stays entirely with
  the caller, which authorizes before it reaches this endpoint.
- **The cache can never fail a render.** A read error or an unreachable bucket is logged and the page
  is rendered; writes happen after the response, off the request path, and are dropped when the
  upload queue is full. Startup does not fail on a cache misconfiguration either.

Requests without a usable `version` are neither read from nor written to the cache: with no version,
content changing under the same source object would keep serving the render of its previous state.
Since the value lands verbatim in an object key, a version containing anything outside
`[A-Za-z0-9._-]`, or longer than 128 characters, is treated the same way — it disables caching for
that request rather than reshaping the key layout, and never fails the render.

### Bucket requirements

Nothing in this service deletes. **The bucket must have a lifecycle rule expiring the objects**,
otherwise it grows forever. Because the keys are content-addressed there is no staleness risk at any
expiry, so the rule is purely a cost/hit-rate dial; the reuse window that matters is how long an
envelope stays in signing, which is days rather than hours.

The instance needs `s3:GetObject` and `s3:PutObject` on the bucket. Without `s3:ListBucket` a missing
key answers `403` rather than `404`, which still degrades to a miss but logs a warning on every cold
page rather than staying silent.

### Observability

- `x-lazyraster-page-cache: hit|miss` on every `/render` response.
- `Worker.Render` spans carry `pageCache.key`, `pageCache.enabled` and `pageCache.hit`. Comparing the
  cardinality of `pageCache.key` against the request count is how the achievable hit rate is measured.
