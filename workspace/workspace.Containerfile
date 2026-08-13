FROM docker.io/library/debian:stable-slim
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      bash ca-certificates curl git htop less nano openssh-sftp-server procps rsync vim \
      sqlite3 \
      python3 python3-venv python3-pip \
      golang-go \
      nodejs npm \
      php-cli \
    && rm -rf /var/lib/apt/lists/*
# System-wide shell niceties so every login shell (SSH and the web terminal)
# gets the usual colours and ll/la aliases, without a dotfile in the app's home.
# Written from base64 so the escapes survive the Containerfile (a heredoc RUN is
# not portable across buildah versions); the decoded file is:
#   alias ls='ls --color=auto'; alias ll='ls -alF'; alias la='ls -A'; alias l='ls -CF'
#   alias grep='grep --color=auto'; dircolors; a coloured PS1
RUN echo YWxpYXMgbHM9J2xzIC0tY29sb3I9YXV0bycKYWxpYXMgbGw9J2xzIC1hbEYnCmFsaWFzIGxhPSdscyAtQScKYWxpYXMgbD0nbHMgLUNGJwphbGlhcyBncmVwPSdncmVwIC0tY29sb3I9YXV0bycKWyAteCAvdXNyL2Jpbi9kaXJjb2xvcnMgXSAmJiBldmFsICIkKGRpcmNvbG9ycyAtYikiCmV4cG9ydCBQUzE9J1xbXDAzM1swMTszMm1cXVx1QFxoXFtcMDMzWzAwbVxdOlxbXDAzM1swMTszNG1cXVx3XFtcMDMzWzAwbVxdXCQgJwo= | base64 -d > /etc/profile.d/hostit.sh
CMD ["/bin/bash"]
