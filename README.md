# Lazyraster
Lazyraster is a HTTP service to convert PDF pages into PNG built on top of <a href="https://github.com/nitro/lazypdf">lazypdf</a>. Converted pages are kept on a encrypted cache layer at AWS S3.

## Run
Environment variables:
| Options                 | Description                                                                           |
| ----------------------- | ------------------------------------------------------------------------------------- |
| `CACHE_BUCKET`          | Name of the bucket where the cache is kept.                                           |
| `CACHE_SECRET`          | Secret used to encrypt and decrypt the cache. This is an AES key.                     |
| `URL_SIGNING_SECRET`    | Secret used to check if the request is valid.                                         |
| `ENABLE_DATADOG`        | Enable Datadog.                                                                       |
| `STORAGE_BUCKET_REGION` | Map of the region a bucket belongs to: `eu-west-1:bucket1,bucket2;us-west-1:bucket3`. |

```go
go run cmd/main.go
```

## Testing
```go
go test -v -race -cover ./...
```
