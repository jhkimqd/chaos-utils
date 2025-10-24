# docker build . \
#   --tag jhkimqd/comcast:latest \
#   --file ./Dockerfile
FROM golang:1.23 AS builder

WORKDIR /opt
COPY go.mod ./
RUN go mod download

COPY . .
RUN make clean \
    && make default

FROM ubuntu:22.04

# Install required system tools for network throttling
RUN apt-get update && apt-get install -y \
    sudo \
    iproute2 \
    iptables \
    net-tools \
    iputils-ping \
    make \
    curl \
    jq \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt

COPY --from=builder /opt/bin/comcast /usr/local/bin/comcast

# Install Toxiproxy
RUN curl -L https://github.com/Shopify/toxiproxy/releases/download/v2.5.0/toxiproxy-server-linux-amd64 -o /usr/local/bin/toxiproxy-server && \
    chmod +x /usr/local/bin/toxiproxy-server && \
    curl -L https://github.com/Shopify/toxiproxy/releases/download/v2.5.0/toxiproxy-cli-linux-amd64 -o /usr/local/bin/toxiproxy-cli && \
    chmod +x /usr/local/bin/toxiproxy-cli

# Note: This container needs to run with --privileged or --cap-add=NET_ADMIN
# to modify network settings
# ENTRYPOINT ["comcast"]
CMD ["/bin/bash"]