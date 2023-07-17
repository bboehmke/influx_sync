FROM golang:1.20

COPY . /src/
WORKDIR /src/

RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /influx_sync .

# use edge image for higher client versions
FROM scratch

# copy app from build image
COPY --from=0 /influx_sync /influx_sync

CMD "/influx_sync"