FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w -X github.com/AndreyKarm/TwitchMiner/internal/config.Version=${VERSION}" \
	-o /twitchminer ./cmd/twitchminer
# The runtime image has no shell, so prepare the data folder here.
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /twitchminer /twitchminer
COPY --from=build --chown=nonroot:nonroot /data /data
ENV TWITCHMINER_ADDR=0.0.0.0:8080 \
	TWITCHMINER_DATA=/data \
	TWITCHMINER_NO_BROWSER=1
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/twitchminer", "-healthcheck"]
ENTRYPOINT ["/twitchminer"]
