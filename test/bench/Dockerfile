# Isolated test bench for ngx's remote path.
#
# Oracle Linux 9 as the base because the shape the bench has to reproduce was
# measured on a production nginx running Oracle Linux 9 / nginx 1.20.1: this
# is the family that brings the layout of three wildcard patterns (modules/,
# conf.d/, default.d/) and an nginx compiled with --modules-path, which gives
# the third wildcard for free.
FROM oraclelinux:9

# nginx from appstream (stream 1.20, the same as the measured production) plus
# the dynamic modules, which are what populates /usr/share/nginx/modules/*.conf
# with real files. openssh-server and sudo complete the target: an ordinary
# user plus explicit escalation.
# Disabling the `nginx` module lets the non-modular package from ol9_appstream
# win, which is nginx 1.20.1 -- the same version measured in production.
# Without that, one day the default stream turns 1.26 and the bench changes on
# its own.
RUN dnf -y module disable nginx \
    && dnf -y install --setopt=install_weak_deps=False \
        nginx \
        nginx-mod-stream \
        nginx-mod-mail \
        nginx-mod-http-image-filter \
        nginx-mod-http-xslt-filter \
        openssh-server \
        sudo \
        openssl \
    && dnf clean all \
    && rm -rf /var/cache/dnf

# The test user is an ordinary user: never root, no password, key only.
# Passwordless sudo is restricted to nginx on purpose -- it is the trap from
# DR5: the privilege exists, but only whoever asks for it explicitly uses it.
#
# The second rule is what PRIVILEGED WRITING requires, and it is in a separate
# file so the difference is visible: `install`, `dd`, `mv` and `rm` under sudo
# are, together, equivalent to full root. There is no way to scope `sudo dd` to
# /etc/nginx -- it writes anywhere. The bench carries the rule because the
# feature has to be tested against a real sudo, and docs/install-channels.md
# says plainly that granting it is granting root, and that owning the files is
# the alternative that is not.
RUN useradd --create-home --shell /bin/bash ngxtest \
    && printf 'ngxtest ALL=(root) NOPASSWD: /usr/sbin/nginx\n' > /etc/sudoers.d/ngxtest \
    && printf 'ngxtest ALL=(root) NOPASSWD: /usr/bin/install, /usr/bin/dd, /usr/bin/mv, /usr/bin/rm, /usr/bin/true\n' > /etc/sudoers.d/ngxtest-write \
    && chmod 0440 /etc/sudoers.d/ngxtest-write \
    && chmod 0440 /etc/sudoers.d/ngxtest \
    && passwd -l root \
    && usermod -p '*' ngxtest

COPY sshd_config.bench /etc/ssh/sshd_config
COPY generate-config.sh /usr/local/bin/generate-config.sh
COPY entrypoint.sh /usr/local/bin/entrypoint.sh

# The configuration is generated in the image, not committed: 130 files written
# by hand would be noise in the repository and a lie about what is being tested.
ARG NGX_BENCH_FILES=130
RUN chmod 0755 /usr/local/bin/generate-config.sh /usr/local/bin/entrypoint.sh \
    && chmod 0600 /etc/ssh/sshd_config \
    && /usr/local/bin/generate-config.sh "${NGX_BENCH_FILES}"

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
