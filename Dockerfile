# syntax=docker/dockerfile:1

# The admin panel (web/admin), built into dist/app and embedded in the binary.
FROM node:22-alpine AS web
WORKDIR /web
COPY web/admin/package.json web/admin/package-lock.json ./
RUN npm ci
COPY web/admin/ ./
RUN npm run build

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY web/admin/go.mod web/admin/
RUN go mod download
COPY . .
COPY --from=web /web/dist/app web/admin/dist/app
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/makonbook ./cmd/makonbook

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/makonbook /makonbook
EXPOSE 8080
ENTRYPOINT ["/makonbook"]
CMD ["serve"]
