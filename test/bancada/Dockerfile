# Bancada de teste isolada para o caminho remoto do ngx.
#
# Base Oracle Linux 9 porque a forma que a bancada precisa reproduzir foi
# medida num nginx de producao Oracle Linux 9 / nginx 1.20.1: e desta familia
# que vem o layout de tres padroes com curinga (modules/, conf.d/, default.d/)
# e o nginx compilado com --modules-path, que da o terceiro curinga de graca.
FROM oraclelinux:9

# nginx do appstream (stream 1.20, o mesmo da producao medida) mais os modulos
# dinamicos, que sao o que povoa /usr/share/nginx/modules/*.conf com arquivos
# reais. openssh-server e sudo completam o alvo: usuario comum + escalada
# explicita.
# Desabilitar o modulo `nginx` deixa valer o pacote nao-modular do
# ol9_appstream, que e o nginx 1.20.1 -- a mesma versao medida em producao.
# Sem isso, um dia o stream padrao vira 1.26 e a bancada muda sozinha.
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

# Usuario de teste e um usuario comum: nunca root, sem senha, so chave.
# O sudo sem senha e restrito ao nginx de proposito -- e a armadilha da DR5:
# o privilegio existe, mas so quem pede explicitamente o usa.
RUN useradd --create-home --shell /bin/bash ngxtest \
    && printf 'ngxtest ALL=(root) NOPASSWD: /usr/sbin/nginx\n' > /etc/sudoers.d/ngxtest \
    && chmod 0440 /etc/sudoers.d/ngxtest \
    && passwd -l root \
    && usermod -p '*' ngxtest

COPY sshd_config.bancada /etc/ssh/sshd_config
COPY gerar-config.sh /usr/local/bin/gerar-config.sh
COPY entrypoint.sh /usr/local/bin/entrypoint.sh

# A configuracao e gerada na imagem, nao commitada: 130 arquivos escritos a mao
# seriam ruido no repositorio e mentira sobre o que esta sendo testado.
ARG NGX_BANCADA_ARQUIVOS=130
RUN chmod 0755 /usr/local/bin/gerar-config.sh /usr/local/bin/entrypoint.sh \
    && chmod 0600 /etc/ssh/sshd_config \
    && /usr/local/bin/gerar-config.sh "${NGX_BANCADA_ARQUIVOS}"

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
