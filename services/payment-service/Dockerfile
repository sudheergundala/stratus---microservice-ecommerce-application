# ------------------- Stage1 : Build ------------------------------------
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/payment-service ./cmd/server

# ---------------------Stage2 : runtime -------------------------------

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /out/payment-service /payment-service
USER 65532:65532
EXPOSE 8083
ENTRYPOINT [ "/payment-service" ]