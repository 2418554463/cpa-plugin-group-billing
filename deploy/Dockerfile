# Build the plugin, not a second CPA host. Export /out via the artifact stage.
FROM --platform=linux/amd64 golang:1.27.1-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go test ./internal/... \
    && mkdir /out \
    && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildvcs=false -tags cshared -buildmode=c-shared -ldflags="-s -w" -o /out/cpa-key-billing.so ./cmd/cpa-key-billing

FROM scratch AS artifact
COPY --from=builder /out/cpa-key-billing.so /
