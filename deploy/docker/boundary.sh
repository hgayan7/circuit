#!/bin/sh
set -eu

# Only the trusted boundary has NET_ADMIN. The agent joins after this succeeds.
iptables -P INPUT DROP
iptables -P FORWARD DROP
iptables -P OUTPUT DROP
ip6tables -P INPUT DROP
ip6tables -P FORWARD DROP
ip6tables -P OUTPUT DROP
iptables -A INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A OUTPUT -d "$CIRCUIT_GATEWAY_IP" -p tcp --dport 8443 -j ACCEPT
# Docker's embedded DNS DNAT runs before filter OUTPUT. Drop its resolver in raw.
iptables -t raw -A OUTPUT -d 127.0.0.11 -j DROP
touch /tmp/ready
exec sleep infinity
