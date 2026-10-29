# Build context is the REPOSITORY ROOT (the service uses ../platform-go and ../proto via `replace`).
#   docker build -f order-service/build/Dockerfile --build-arg TARGET=order-service -t ecom/order-service:local .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform-go ./platform-go
COPY proto ./proto
COPY order-service ./order-service
WORKDIR /src/order-service
# TARGET = order-service | console
ARG TARGET=order-service
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${TARGET}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
