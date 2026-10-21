# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/faztudo .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/faztudo /faztudo
ENV ADDR=0.0.0.0:8081 DRY_RUN=true
EXPOSE 8081
USER nonroot:nonroot
ENTRYPOINT ["/faztudo"]
