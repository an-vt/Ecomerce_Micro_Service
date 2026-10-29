# Build context is the REPOSITORY ROOT (the service uses ../platform-go and ../proto via `replace`).
#   docker build -f catalog-service/build/Dockerfile --build-arg TARGET=catalog-service -t ecom/catalog-service:local .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY platform-go ./platform-go
COPY proto ./proto
COPY catalog-service ./catalog-service
WORKDIR /src/catalog-service
# TARGET = catalog-service | console
ARG TARGET=catalog-service
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./cmd/${TARGET}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot:nonroot
ENTRYPOINT ["/app"]
