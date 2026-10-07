# ------------------- Stage1 : Build ------------------------------------
FROM golang:1.26-alpine AS builder
WORKDIR /src
# we are copying the dependencies before the source code, because the layer stayed cached due to least change. If we copy the source code first the build will re-run everything as the cached layer will change. 
COPY go.mod go.sum* ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/payment-service ./cmd/server

# ---------------------Stage2 : runtime -------------------------------
# the scratch has no CA certificates so we used static distroless
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=builder /out/payment-service /payment-service
USER 65532:65532
EXPOSE 8083
ENTRYPOINT [ "/payment-service" ]