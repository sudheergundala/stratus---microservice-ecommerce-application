# ---------- build stage ----------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first: this layer is reused until go.mod or go.sum changes.
# go mod verify checks every module against the hashes in go.sum.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# Source code last: it changes most often.
COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Static binary (no C library), no local paths, no debug symbols.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/inventory-service ./cmd/server

# ---------- runtime stage ----------
# static-debian13 includes CA certificates, needed for HTTPS calls to AWS.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/inventory-service /inventory-service
USER 65532:65532
EXPOSE 8084
ENTRYPOINT ["/inventory-service"]