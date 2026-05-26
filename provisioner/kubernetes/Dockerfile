FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    openssh-server \
    curl \
    xz-utils \
    sudo \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Install Nix at image build time (single-user for containers, no init system)
RUN curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix \
    | sh -s -- install linux \
        --init none \
        --no-confirm \
        --extra-conf "sandbox = false" \
        --extra-conf "max-jobs = auto" \
        --extra-conf "cores = 0"

ENV PATH="/nix/var/nix/profiles/default/bin:/root/.nix-profile/bin:${PATH}"

# Generate SSH host keys once per image (acceptable for ephemeral builders;
# pool.go already uses InsecureIgnoreHostKey when connecting)
RUN mkdir -p /var/run/sshd && ssh-keygen -A

RUN sed -i \
    -e 's/#\?PermitRootLogin.*/PermitRootLogin prohibit-password/' \
    -e 's/#\?PasswordAuthentication.*/PasswordAuthentication no/' \
    -e 's/#\?PubkeyAuthentication.*/PubkeyAuthentication yes/' \
    /etc/ssh/sshd_config && \
    echo "AuthorizedKeysFile /root/.ssh/authorized_keys" >> /etc/ssh/sshd_config && \
    echo "StrictModes no" >> /etc/ssh/sshd_config

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 22

ENTRYPOINT ["/entrypoint.sh"]
