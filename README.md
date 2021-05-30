<p align="center">
  <img src="misc/assets/mascot.png" align="center" height="328.5px" width="250px" />
</p>
<h1 align="center">Lazyraster</h1>
Lazyraster is a HTTP service to convert PDF pages into PNG built on top of <a href="https://github.com/nitro/lazypdf">lazypdf</a>. Converted pages are kept on a encrypted cache layer at AWS S3.

## Run
Environment variables:
| Options              | Description                                                       |
| -------------------- | ----------------------------------------------------------------- |
| `CACHE_BUCKET`       | Name of the bucket where the cache is kept.                       |
| `CACHE_SECRET`       | Secret used to encrypt and decrypt the cache. This is an AES key. |
| `URL_SIGNING_SECRET` | Secret used to check if the request is valid.                     |
| `ENABLE_DATADOG`     | Enable Datadog.                                                   |

```go
go run cmd/main.go
```

## Testing
```go
go test -v -race -cover ./...
```
