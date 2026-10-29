# Build context is the REPOSITORY ROOT (the service uses ../platform-go and ../proto via `replace`).
#   docker build -f api-gateway/build/Dockerfile --build-arg TARGET=api-gateway -t ecom/api-gateway:local .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform-go ./platform-go
COPY proto ./proto
COPY api-gateway ./api-gateway
WORKDIR /src/api-gateway
# TARGET = api-gateway | console
ARG TARGET=api-gateway
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${TARGET}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
